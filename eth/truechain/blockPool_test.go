package truechain


import (
	"testing"
	"fmt"
	"github.com/ethereum/go-ethereum/crypto/sha3"
	"github.com/ethereum/go-ethereum/rlp"
)



func TestStrictTxListAdd(t *testing.T) {


	fmt.Print("start")

}

func rlpHash1(x interface{}) (h []byte) {
	hw := sha3.NewKeccak256()
	rlp.Encode(hw, x)
	hw.Sum(h[:0])
	return h
}


func TestRlpHasha(t *testing.T) {
	//h := make([]byte,32,32)
	x :="hello"
	h :=rlpHash1(x)
	fmt.Print(h)
}

