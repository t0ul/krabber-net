package main

import (
	"testing"

	"github.com/t0ul/krabber-net/internal/writing"
)

// Every seed follows its form's exact rules, so the seeds stand when the
// site requires a form.
func TestSeedsFitTheirForms(t *testing.T) {
	all := map[string]seed{"barnacles": barnacles, "rocks": rocks, "sinceSix": sinceSix}
	for name, sd := range molts {
		all[name] = sd
	}
	for name, sd := range all {
		f, ok := writing.ByID(sd.form)
		if !ok || sd.form == "" {
			t.Errorf("%s: no form %q", name, sd.form)
			continue
		}
		if broken := writing.Broken(f.Check(sd.text)); len(broken) > 0 {
			t.Errorf("%s is not a %s: %s (%s)", name, f.Name, broken[0].Rule, broken[0].Note)
		}
	}
}
