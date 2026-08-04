package connector

import "testing"

func TestKeyFingerprintCanonicalizesCommonForms(t *testing.T) {
	want := KeyFingerprint("token-value")
	for _, value := range []string{"Bearer sk-token-value", "sk-token-value", "token-value"} {
		if got := KeyFingerprint(value); got != want {
			t.Fatalf("KeyFingerprint(%q) = %q, want %q", value, got, want)
		}
	}
}

func TestKeyFingerprintReturnsEmptyForEmptyKey(t *testing.T) {
	if got := KeyFingerprint(" Bearer sk- "); got != "" {
		t.Fatalf("KeyFingerprint(empty) = %q, want empty", got)
	}
}
