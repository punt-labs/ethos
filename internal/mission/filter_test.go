package mission

import (
	"testing"

	"github.com/stretchr/testify/assert"
)

func TestStatusMatches(t *testing.T) {
	tests := []struct {
		name           string
		filter         string
		contractStatus string
		want           bool
	}{
		{"empty filter matches open", "", StatusOpen, true},
		{"empty filter matches closed", "", StatusClosed, true},
		{"all matches open", "all", StatusOpen, true},
		{"all matches closed", "all", StatusClosed, true},
		{"all matches failed", "all", StatusFailed, true},
		{"all matches escalated", "all", StatusEscalated, true},
		{"open matches open", StatusOpen, StatusOpen, true},
		{"open does not match closed", StatusOpen, StatusClosed, false},
		{"closed matches closed", StatusClosed, StatusClosed, true},
		{"closed does not match open", StatusClosed, StatusOpen, false},
		{"failed matches failed", StatusFailed, StatusFailed, true},
		{"escalated matches escalated", StatusEscalated, StatusEscalated, true},
		{"unknown filter matches nothing", "bogus", StatusOpen, false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			assert.Equal(t, tt.want, StatusMatches(tt.filter, tt.contractStatus))
		})
	}
}
