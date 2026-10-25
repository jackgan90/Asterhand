package network

import "testing"

func TestListenerValidationAndEffectiveState(t *testing.T) {
	for _, value := range []string{"127.0.0.1:17433", "100.121.127.89:17433", "[::1]:17433", "0.0.0.0:17433"} {
		if err := ValidateListener(value); err != nil {
			t.Fatal(value, err)
		}
	}
	for _, value := range []string{"", "localhost:17433", ":17433", "127.0.0.1:0", "127.0.0.1:65536"} {
		if ValidateListener(value) == nil {
			t.Fatal("accepted", value)
		}
	}
	if !ListenerMatches("0.0.0.0:17433", "[::]:17433") || !ListenerMatches("127.0.0.1:0", "127.0.0.1:31415") {
		t.Fatal("wildcard or ephemeral listener mismatch")
	}
	if ListenerMatches("100.121.127.89:17433", "127.0.0.1:17433") {
		t.Fatal("pending network change treated as active")
	}
	if ConnectionWarning("https://100.121.127.89:17433", "100.121.127.89:17433", "100.121.127.89:17433") != "" {
		t.Fatal("valid connection warns")
	}
	if ConnectionWarning("https://127.0.0.1:17433", "127.0.0.1:17433", "127.0.0.1:17433") == "" {
		t.Fatal("loopback not explained")
	}
	if !LoopbackEndpoint("https://LOCALHOST:17433") {
		t.Fatal("case bypass")
	}
}
