#!/bin/bash
# TLS between CloudFront and this instance (infra/prod/origin_tls.tf).
# Exports the origin certificate from ACM and adds nginx's port 443 server.
# Runs before every deploy; a daily timer reruns it with --refresh to pick up
# ACM's renewals (45 days before expiry). Without ORIGIN_CERT_ARN the origin
# stays HTTP-only.
set -euo pipefail

get() { /opt/elasticbeanstalk/bin/get-config environment -k "$1" 2>/dev/null || true; }

arn=$(get ORIGIN_CERT_ARN)
if [ -z "$arn" ]; then
  echo "ORIGIN_CERT_ARN isn't set; the origin stays HTTP-only"
  exit 0
fi

dir=/etc/pki/krabber
install -d -m 700 "$dir"
tmp=$(mktemp -d)
trap 'rm -rf "$tmp"' EXIT

# The key comes back encrypted with a one-off passphrase.
openssl rand -hex 32 | tr -d '\n' >"$tmp/pass"
aws acm export-certificate --region "$(get AWS_REGION)" --certificate-arn "$arn" \
  --passphrase "fileb://$tmp/pass" --output json >"$tmp/export.json"
python3 - "$tmp" <<'EOF'
import json, sys
d = sys.argv[1]
o = json.load(open(d + "/export.json"))
with open(d + "/origin.crt", "w") as f:
    f.write(o["Certificate"].strip() + "\n" + o["CertificateChain"].strip() + "\n")
with open(d + "/encrypted.key", "w") as f:
    f.write(o["PrivateKey"])
EOF
openssl pkey -in "$tmp/encrypted.key" -passin "file:$tmp/pass" -out "$tmp/origin.key"
install -m 600 "$tmp/origin.key" "$dir/origin.key"
install -m 644 "$tmp/origin.crt" "$dir/origin.crt"

if [ "${1:-}" = "--refresh" ]; then
  exit 0
fi

install -m 755 "$0" /usr/local/sbin/krabber-origin-cert
cat >/etc/systemd/system/krabber-origin-cert.service <<'EOF'
[Unit]
Description=Refresh the origin certificate from ACM

[Service]
Type=oneshot
ExecStart=/usr/local/sbin/krabber-origin-cert --refresh
ExecStartPost=/bin/systemctl reload nginx
EOF
cat >/etc/systemd/system/krabber-origin-cert.timer <<'EOF'
[Unit]
Description=Refresh the origin certificate daily

[Timer]
OnCalendar=daily
RandomizedDelaySec=1h
Persistent=true

[Install]
WantedBy=timers.target
EOF
systemctl daemon-reload
systemctl enable --now krabber-origin-cert.timer

# Written into the bundle being deployed (the hook runs in /var/app/staging),
# so nginx only gets a 443 server once the certificate is on disk. The
# healthd log keeps enhanced health reporting on the requests it serves.
cat >.platform/nginx/conf.d/origin_https.conf <<'EOF'
server {
    listen 443 ssl;
    listen [::]:443 ssl;
    ssl_certificate     /etc/pki/krabber/origin.crt;
    ssl_certificate_key /etc/pki/krabber/origin.key;
    ssl_protocols       TLSv1.2 TLSv1.3;
    ssl_session_cache   shared:origin:1m;

    if ($time_iso8601 ~ "^(\d{4})-(\d{2})-(\d{2})T(\d{2})") {
        set $year $1;
        set $month $2;
        set $day $3;
        set $hour $4;
    }
    access_log /var/log/nginx/healthd/application.log.$year-$month-$day-$hour healthd;

    location / {
        proxy_pass         http://127.0.0.1:5000;
        proxy_http_version 1.1;
        proxy_set_header   Connection "";
        proxy_set_header   Host $host;
        proxy_set_header   X-Real-IP $remote_addr;
        proxy_set_header   X-Forwarded-For $proxy_add_x_forwarded_for;
    }
}
EOF
