package syncer

import (
	"math/big"
	"testing"
)

func TestSplitBigIntRanges(t *testing.T) {
	minV := big.NewInt(1)
	maxV := big.NewInt(100)
	ranges := splitBigIntRanges(minV, maxV, 4)
	if len(ranges) != 4 {
		t.Fatalf("got %d ranges", len(ranges))
	}
	if ranges[0][0].Cmp(big.NewInt(1)) != 0 {
		t.Fatalf("first lo=%s", ranges[0][0])
	}
	if ranges[len(ranges)-1][1].Cmp(big.NewInt(100)) != 0 {
		t.Fatalf("last hi=%s", ranges[len(ranges)-1][1])
	}
	// ranges should be contiguous: next.lo == prev.hi for non-last exclusive scheme
	for i := 0; i < len(ranges)-1; i++ {
		if ranges[i+1][0].Cmp(ranges[i][1]) != 0 {
			t.Fatalf("gap/overlap at %d: %s vs %s", i, ranges[i][1], ranges[i+1][0])
		}
	}
}

func TestToBigInt(t *testing.T) {
	cases := []any{int64(42), uint64(99), []byte("123"), "456"}
	for _, c := range cases {
		z, ok := toBigInt(c)
		if !ok || z == nil {
			t.Fatalf("toBigInt(%v) failed", c)
		}
	}
}

func TestIsIntegerMySQLType(t *testing.T) {
	if !isIntegerMySQLType("bigint unsigned") {
		t.Fatal("expected bigint")
	}
	if isIntegerMySQLType("varchar(64)") {
		t.Fatal("varchar should not be integer")
	}
}
