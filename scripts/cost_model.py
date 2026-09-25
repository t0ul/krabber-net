#!/usr/bin/env python3
"""Krabber's monthly AWS bill at a given number of daily active krabs (DAU).

Per-request DynamoDB units come from TestCostProfile (internal/web/cost_test.go),
plus the index writes DynamoDB Local doesn't report and the background work
(notifications, fan-out, the hourly directory reload, suggestions). The usage
profile is an assumption; change it and rerun:

    python3 scripts/cost_model.py 1000 5000 10000
"""

import sys

# Prices, us-east-2, on-demand (checked 2026-09-25).
RRU = 0.125 / 1e6  # per read request unit
WRU = 0.625 / 1e6  # per write request unit
STORAGE = 0.25  # per GB-month past the free 25 GB
PITR = 0.20  # per GB-month of base table
DAYS = 30

# What one daily active krab does in a day (assumption).
USAGE = {
    "feed_pages": 10,  # Trench, Sea, krabtag, profile, thread
    "other_pages": 3,  # notifications, settings, search, stats
    "load_more": 4,
    "active_minutes": 20,  # tab visible and in use; one poll a minute
    "likes": 8,
    "molts": 0.5,  # originals
    "replies": 0.5,
    "remolts": 0.1,
    "follows": 0.3,
    "bookmarks": 0.2,
    "logins": 0.5,  # sessions last 12 hours
    "new_avatars": 10,  # distinct avatar images a browser hasn't cached
}
FANOUT = 50  # followers per molt (one trench write each)
SIGNED_OUT = 0.2  # signed-out page views, as a share of signed-in ones
KRABS_PER_DAU = 3  # accounts per daily active krab (for the directory scan)

# Units per request, after the 2026-09-25 changes (measured) and before them.
AFTER = {
    "feed_page": (18, 0), "other_page": (6, 0.3), "load_more": (15, 0),
    "poll": (3, 0), "signed_out_page": (8, 0),
    "like": (6, 5 + 3),  # like transaction + GSI7, then marker, notification, counter
    "molt": (2, 17), "reply": (6, 9 + 2), "remolt": (6, 14 + 3),
    "follow": (2.5, 11 + 3), "bookmark": (6, 5), "login": (4, 3),
}
BEFORE = {
    "feed_page": (46, 0), "other_page": (10, 1), "load_more": (45, 0),
    # 30 s new-molts poll with a full sidebar, plus the 60 s badge poll,
    # running in background tabs too (throttled by browsers to about once a
    # minute): about 3 active-equivalent polls per active minute.
    "poll": (4.5, 0), "signed_out_page": (12.5, 0),
    "like": (6, 5 + 6), "molt": (4, 17), "reply": (8, 9 + 4), "remolt": (6, 14 + 6),
    "follow": (14.5, 11 + 6), "bookmark": (6, 5), "login": (4, 3),
}
POLLS_PER_ACTIVE_MINUTE = {"after": 1, "before": 3 * 3}  # before: 2 polls/min, open 3x as long


def dynamo(dau, units, polls_per_minute, reload_per_day):
    u = USAGE
    per_dau = {
        "feed_page": u["feed_pages"], "other_page": u["other_pages"], "load_more": u["load_more"],
        "poll": u["active_minutes"] * polls_per_minute,
        "signed_out_page": (u["feed_pages"] + u["other_pages"]) * SIGNED_OUT,
        "like": u["likes"], "molt": u["molts"], "reply": u["replies"], "remolt": u["remolts"],
        "follow": u["follows"], "bookmark": u["bookmarks"], "login": u["logins"],
    }
    reads = sum(n * units[k][0] for k, n in per_dau.items()) * dau
    writes = sum(n * units[k][1] for k, n in per_dau.items()) * dau
    writes += (u["molts"] + u["remolts"]) * FANOUT * dau  # trench fan-out
    krabs = dau * KRABS_PER_DAU
    reads += reload_per_day(krabs)
    reads += dau * 1.5 * 20  # friends-of-friends: 20 follow lists per viewer, 1.5 times a day
    requests = sum(per_dau.values()) * dau + dau * u["new_avatars"]
    return reads * DAYS, writes * DAYS, requests * DAYS


def storage_gb(dau, months):
    permanent_kb = (1.1 * 1.0 + USAGE["likes"] * 0.8 + 0.3 * 0.7 + 0.2 * 0.5)  # per DAU-day
    rolling_kb = ((USAGE["molts"] + USAGE["remolts"]) * FANOUT * 0.3 + 10 * 0.5) * 90
    return (permanent_kb * dau * 30 * months + rolling_kb * dau) / 1e6


def edge(requests):
    """The flat-rate plan to be on, its price and the share of its allowance
    used. Going over isn't billed; AWS may slow delivery after months of
    substantial excess, so past Pro's allowance the pay-as-you-go price is the
    alternative (payg below)."""
    if requests <= 1e6:
        return "Free", 0, requests / 1e6
    return "Pro", 15, requests / 10e6


def payg(requests):
    """CloudFront and WAF on pay-as-you-go: requests past the free 10M at
    $1.00 per million, the web ACL ($5) and 5 rules ($1 each), WAF requests at
    $0.60 per million, and the hosted zone. Data transfer stays inside the free
    1 TB at these sizes."""
    return max(requests - 10e6, 0) / 1e6 + 10 + 0.6 * requests / 1e6 + 0.5


def report(dau):
    rows = []
    for label, units, ppm, reload in (
        # Before: the scan stopped at 500 krabs (one 1 MB page, about 125
        # units) plus 200 molts, every 2 minutes and again after each follow.
        ("before", BEFORE, POLLS_PER_ACTIVE_MINUTE["before"],
         lambda krabs: min(8640, 720 + dau * USAGE["follows"]) * (min(krabs, 1000) / 8 + 100)),
        ("after", AFTER, POLLS_PER_ACTIVE_MINUTE["after"],
         lambda krabs: 24 * (krabs / 8 + 500)),
    ):
        reads, writes, requests = dynamo(dau, units, ppm, reload)
        gb = storage_gb(dau, 12)
        store_cost = max(gb - 25, 0) * STORAGE + gb * 0.75 * PITR
        plan, plan_cost, share = edge(requests)
        ec2 = 6.13 + 3.65 + 0.64 if dau <= 5000 else 12.26 + 3.65 + 0.64
        base = reads * RRU + writes * WRU + store_cost + ec2 + 1.5  # logs, SES, misc
        rows.append((label, reads * RRU, writes * WRU, store_cost, plan, plan_cost, share, ec2,
                     base + plan_cost, base + payg(requests), requests))
    print(f"\n{dau:,} daily active krabs ({dau * KRABS_PER_DAU:,} accounts), storage after a year")
    print(f"{'':7} {'reads':>7} {'writes':>7} {'storage':>7} {'plan (allowance)':>17} {'EC2':>6} "
          f"{'total':>7} {'or PAYG':>8} {'requests':>9}")
    for label, r, w, s, plan, pc, share, ec2, total, alt, req in rows:
        print(f"{label:7} {r:7.2f} {w:7.2f} {s:7.2f} {plan + f' ${pc:.0f} ({share:.0%})':>17} {ec2:6.2f} "
              f"{total:7.2f} {alt:8.2f} {req / 1e6:8.1f}M")


if __name__ == "__main__":
    for arg in sys.argv[1:] or ["1000", "5000", "10000"]:
        report(int(arg))
