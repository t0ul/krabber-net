package web

import (
	"strings"
	"testing"
	"time"

	"github.com/t0ul/krabber-net/internal/writing"
)

// @system's molts are haikus the site's own counter accepts, whatever the
// version hash, the counts or the uptime.
func TestSystemMoltsAreHaikus(t *testing.T) {
	haiku, _ := writing.ByID(systemForm)
	check := func(name, text string) {
		t.Helper()
		if broken := writing.Broken(haiku.Check(text), false); len(broken) > 0 {
			t.Errorf("%s: %s (%s)\n%s", name, broken[0].Rule, broken[0].Note, text)
		}
	}
	// Hashes whose letters count as none, one, two or three syllables, and a dev build.
	for _, v := range []string{"dede999", "bcd41d7", "1234567", "abcdef0", "deadbee", "f144dde", "aeb0c1d", "dev"} {
		m := versionMolt(v)
		check("version "+v, m)
		if !strings.HasPrefix(m, "a new Krabber build\n") || !strings.Contains(m, v) || !strings.HasSuffix(m, "\n%status: all afloat") {
			t.Errorf("version %s: %q", v, m)
		}
	}
	for _, up := range []time.Duration{0, 12 * time.Minute, 90 * time.Minute, 5 * time.Hour, 47 * time.Hour, 3 * 24 * time.Hour, 400 * 24 * time.Hour} {
		for _, counts := range [][3]string{{"2", "2", "0"}, {"1", "0", "1"}, {"1,204", "17", "200+"}, {"0", "0", "0"}} {
			m := reportMolt(counts[0], counts[1], counts[2], up)
			check("report "+up.String(), m)
			if !strings.HasPrefix(m, "all's well on the reef\n"+counts[0]+" krabs, "+counts[1]+" new, "+counts[2]+" molts since yesterday\nup ") || !strings.HasSuffix(m, " %status") {
				t.Errorf("report %v %s: %q", counts, up, m)
			}
		}
	}
}
