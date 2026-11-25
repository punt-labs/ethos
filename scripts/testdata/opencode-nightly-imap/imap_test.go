package nightlyimap

import (
	"os"
	"os/signal"
	"syscall"
	"testing"

	"github.com/punt-labs/beadle/internal/testserver"
)

func TestServeNightlyIMAP(t *testing.T) {
	stop := make(chan os.Signal, 1)
	signal.Notify(stop, syscall.SIGTERM, os.Interrupt)
	defer signal.Stop(stop)
	_, addr := testserver.NewIMAPServer(t, "nightly@example.com", "nightly-fixture")
	if err := os.WriteFile(os.Getenv("NIGHTLY_IMAP_ADDR_FILE"), []byte(addr), 0o600); err != nil {
		t.Fatal(err)
	}
	<-stop
}
