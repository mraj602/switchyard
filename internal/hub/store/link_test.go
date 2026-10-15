package store

import "testing"

func TestDeliveryRefs(t *testing.T) {
	if r := DeliveryRef([]int64{3, 7}); r != "d:3,7" {
		t.Fatal(r)
	}
	if ids := DeliveryRefs("d:3,7"); len(ids) != 2 || ids[1] != 7 {
		t.Fatal(ids)
	}
	if ids := DeliveryRefs("pm:x"); len(ids) != 0 {
		t.Fatal(ids)
	}
}
