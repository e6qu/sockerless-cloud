package aws_cli_test

import (
	"strings"
	"testing"
	"time"
)

// TestEC2CLI_CapacityReservationDateChangeQuote postpones a future-dated
// Capacity Reservation the way the EC2 user guide describes: quote, review,
// then modify with the quote and its terms accepted.
func TestEC2CLI_CapacityReservationDateChangeQuote(t *testing.T) {
	q := func(args ...string) string { return strings.TrimSpace(runCLI(t, awsCLI(args...))) }
	start := time.Now().Add(20 * 24 * time.Hour).UTC().Truncate(time.Second)
	id := q("ec2", "create-capacity-reservation",
		"--instance-type", "m5.24xlarge", "--instance-platform", "Linux/UNIX",
		"--availability-zone", "us-east-1a", "--instance-count", "2",
		"--instance-match-criteria", "targeted",
		"--start-date", start.Format(time.RFC3339),
		"--commitment-duration", "1209600",
		"--query", "CapacityReservation.CapacityReservationId", "--output", "text")
	if got := q("ec2", "describe-capacity-reservations", "--capacity-reservation-ids", id,
		"--query", "CapacityReservations[0].State", "--output", "text"); got != "scheduled" {
		t.Fatalf("a future-dated reservation is %q, want scheduled", got)
	}

	newStart := start.Add(7 * 24 * time.Hour)
	quote := q("ec2", "create-capacity-reservation-date-change-quote",
		"--capacity-reservation-id", id, "--new-start-date", newStart.Format(time.RFC3339),
		"--query", "CapacityReservationModificationQuote.CapacityReservationModificationQuoteId", "--output", "text")
	if got := q("ec2", "describe-capacity-reservation-date-change-quotes",
		"--capacity-reservation-modification-quote-ids", quote,
		"--query", "CapacityReservationModificationQuotes[0].QuoteState", "--output", "text"); got != "active" {
		t.Fatalf("a new quote is %q, want active", got)
	}

	runCLI(t, awsCLI("ec2", "modify-capacity-reservation", "--capacity-reservation-id", id,
		"--quote-id", quote, "--accept-modification-terms"))
	got := q("ec2", "describe-capacity-reservations", "--capacity-reservation-ids", id,
		"--query", "CapacityReservations[0].[StartDate,AdjustmentStatus]", "--output", "text")
	fields := strings.Fields(got)
	if len(fields) != 2 || fields[1] != "applied" {
		t.Fatalf("after the quote: %q, want the new start date and applied", got)
	}
	if moved, err := time.Parse(time.RFC3339, fields[0]); err != nil || !moved.Equal(newStart) {
		t.Fatalf("start date after the quote = %q, want %s", fields[0], newStart.Format(time.RFC3339))
	}
}
