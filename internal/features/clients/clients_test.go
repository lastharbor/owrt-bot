package clients

import "testing"

func TestParseElements(t *testing.T) {
	out := `table inet owrt_bot {
	set blocked {
		type ether_addr
		flags timeout
		elements = { 02:5e:10:aa:bb:01 timeout 5m expires 4m58s110ms, aa:bb:cc:dd:ee:01 timeout 2h expires 1h59m }
	}
}`
	got := parseElements(out)
	if got["02:5e:10:aa:bb:01"] != "4m58s110ms" || got["aa:bb:cc:dd:ee:01"] != "1h59m" || len(got) != 2 {
		t.Fatalf("got %v", got)
	}
	if len(parseElements("set blocked {\n type ether_addr\n}")) != 0 {
		t.Fatal("empty set parsed as non-empty")
	}
}
