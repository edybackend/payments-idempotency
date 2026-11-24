package payments

import "testing"

func TestValidCard(t *testing.T) {
	tests := map[string]bool{
		"4242424242424242": true,  // approves
		"4000000000000002": true,  // valid number, declined by the acquirer
		"4242424242424241": false, // Luhn fails
		"424242424242424":  false, // 15 digits
		"42424242424242a2": false, // not a digit
	}
	for card, want := range tests {
		if got := validCard(card); got != want {
			t.Errorf("validCard(%q) = %v; want %v", card, got, want)
		}
	}
}
