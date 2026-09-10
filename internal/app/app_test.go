package app_test

import (
	"strings"
	"testing"
	"time"

	"github.com/casperlundberg/simlab-api/internal/app"
)

func env(pairs map[string]string) func(string) string {
	return func(key string) string { return pairs[key] }
}

func complete() map[string]string {
	return map[string]string{
		"SIMLAB_DATABASE_URL":   "postgres://simlab@localhost/simlab",
		"SIMLAB_AUTOSCALER_URL": "http://autoscaler:8080",
	}
}

func TestTheTwoThingsWithoutWhichNothingWorksAreRequired(t *testing.T) {
	_, err := app.LoadConfig(env(nil))
	if err == nil {
		t.Fatal("LoadConfig() = nil error with neither a database nor an autoscaler")
	}
	// A default would produce a service that starts happily and fails every
	// run, which is much harder to diagnose than one that will not start.
	for _, want := range []string{"SIMLAB_DATABASE_URL", "SIMLAB_AUTOSCALER_URL"} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("LoadConfig() = %q, want it to name %q", err, want)
		}
	}
}

func TestACompleteEnvironmentStarts(t *testing.T) {
	got, err := app.LoadConfig(env(complete()))
	if err != nil {
		t.Fatalf("LoadConfig() = %v", err)
	}
	if got.Address == "" {
		t.Error("Address is empty")
	}
	if got.RequestTimeout <= 0 {
		t.Errorf("RequestTimeout = %v", got.RequestTimeout)
	}
}

func TestEverySettingCanBeOverridden(t *testing.T) {
	values := complete()
	values["SIMLAB_ADDRESS"] = ":9100"
	values["SIMLAB_AUTOSCALER_TOKEN"] = "s3cr3t"
	values["SIMLAB_STATIC_DIR"] = "/srv/web"
	values["SIMLAB_REQUEST_TIMEOUT"] = "45s"

	got, err := app.LoadConfig(env(values))
	if err != nil {
		t.Fatalf("LoadConfig() = %v", err)
	}
	if got.Address != ":9100" || got.AutoscalerToken != "s3cr3t" ||
		got.StaticDir != "/srv/web" || got.RequestTimeout != 45*time.Second {
		t.Errorf("LoadConfig() = %+v", got)
	}
}

func TestAnUnreadableDurationIsRefusedAtStartup(t *testing.T) {
	values := complete()
	values["SIMLAB_REQUEST_TIMEOUT"] = "soon"

	if _, err := app.LoadConfig(env(values)); err == nil ||
		!strings.Contains(err.Error(), "SIMLAB_REQUEST_TIMEOUT") {
		t.Errorf("LoadConfig() = %v, want the bad variable named", err)
	}
}
