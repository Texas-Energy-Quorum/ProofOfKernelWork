package pokw

import (
	"bytes"
	"fmt"
	// "hash"
	"math/big"
	"strings"
	"os"

	"github.com/ethereum/go-ethereum/accounts"
	"github.com/ethereum/go-ethereum/common"
	"github.com/ethereum/go-ethereum/consensus/clique"
	"github.com/ethereum/go-ethereum/core/types"
	"github.com/ethereum/go-ethereum/crypto"
	"golang.org/x/crypto/sha3"
	lru "github.com/hashicorp/golang-lru"
)
var (
	taskMineBlock = byte(1)
	max256        = big.NewInt(0) // max 256 bit positive number.
)

func init() {
	var ok bool
	// 256 bit = 64hex
	if max256, ok = max256.SetString(strings.Repeat("f", 64), 16); !ok {
		panic("can't parse max string")
	}
}
// type hasher func(dest []byte, data []byte) 
// func makeHasher(h hash.Hash) hasher {
// 	// sha3.state supports Read to get the sum, use it to avoid the overhead of Sum.
// 	// Read alters the state but we reset the hash before every operation.
// 		type readerHash interface {
// 	hash.Hash
// 	Read([]byte) (int, error)
// }
// 	rh, ok := h.(readerHash)
// 	if !ok {
// 		panic("can't find Read method on hash")
// 	}
// 	outputLen := rh.Size()
// 	return func(dest []byte, data []byte) {
// 		rh.Reset()
// 		rh.Write(data)
// 		rh.Read(dest[:outputLen])
// 	}
// }
// func newPoKWHasher() hasher {
// 	// TODO: use New256 instead of Keccak256 - but the signerFn interface hides the hash function.
// 	// so we can't do it here unless we expose other functions of the wallet.
// 	return makeHasher(sha3.NewLegacyKeccak256())
// }

// Signer wraps signer wallet functions
type Signer struct {
	addr   common.Address  // Ethereum address of the signing key
	signFn clique.SignerFn // Signer function to authorize hashes with, same
}

// Sign returns a signature of a hashed data
func (s Signer) Sign(data []byte) ([]byte, error) {
	// TODO:pokw check if we want to use Clique mimetype
	return s.signFn(accounts.Account{Address: s.addr}, accounts.MimetypeClique, data)
}

// deriveSeed calculates new seed based on the previous block similarly to the algorand VRF.
// @height is this block height (parent.Number + 1)
func deriveSeed(s Signer, parentSeed []byte, height *big.Int, isPoW bool) ([]byte, error) {
	b := height.Bytes()
	if !isPoW {
		return crypto.Keccak256(parentSeed, b), nil
	}
	// NOTE: we diverge from the paper a bit. Check README for more details.
	sig, err := s.Sign(append(parentSeed, b...))
	return sig, wrapErr("can't sign seed", err)
}

func verifySeed(signer common.Address, seed, parentSeed []byte, height *big.Int, isPoW bool) error {
	// hr.Mutex.Lock()
	// defer hr.Mutex.Unlock()
	fmt.Fprintln(os.Stderr, "--- ParentSeed len == ", len(parentSeed))
	b := height.Bytes()
	var msg = make([]byte, common.HashLength)
	hr := makeHasher(sha3.NewLegacyKeccak256())
	hr(append(parentSeed, b...), msg)
	
	if !isPoW {
		if !bytes.Equal(seed, msg) {
			return fmt.Errorf("non-pow block seed must be a hash of previous seed [%w]", errInvalidSeed)
		}
	} else {
		addr, err := ecrecover(msg, seed)
		if err != nil {
			return fmt.Errorf("Can't recover address from header seed [%w]", err)
		}
		if addr != signer {
			return errInvalidSeed
		}
	}
	return nil
}

// assertInCommittee executes secret sorition and return nil if a valid signature
// attest membership in a committee
func assertInCommittee(s Signer, expectedCSize uint32, whitelistSize int32, parentHeight *big.Int, parentSeed []byte) ([]byte, error) {
	msg := committeeSeed(parentHeight, parentSeed)
	// sig, err = crypto.Sign(msg, prv)  // again, we can't sign directly without hashing.
	sig, err := s.Sign(msg)
	if err != nil {
		return sig, wrapErr("can't sign seed", err)
	}
	return sig, verifySigInCommittee(sig, expectedCSize, whitelistSize)
}

func committeeSeed(parentHeight *big.Int, parentSeed []byte) []byte {
	var msg = parentHeight.Bytes()
	msg = append(msg, taskMineBlock)
	return append(msg, parentSeed...)
}

func verifySigInCommittee(sig []byte, expectedCSize uint32, whitelistSize int32) error {
	if expectedCSize < 0 {
		return nil
	}
	if expectedCSize == 0 {
		return ErrNotInCommittee
	}
	hash := crypto.Keccak256(sig)
	if !checkTreshold(hash, expectedCSize, whitelistSize) {
		return ErrNotInCommittee
	}
	return nil
}

func checkTreshold(hash []byte, expected uint32, total int32) bool {
	if expected >= uint32(total) {
		return true
	}
	treshold := new(big.Int)
	treshold.Mul(max256, big.NewInt(int64(expected)))
	treshold.Div(treshold, big.NewInt(int64(total)))
	var x = new(big.Int).SetBytes(hash)
	return treshold.Cmp(x) <= 0
}

const minDifficulty = 1 << 16
const powDifficulty = 1 << 17 // difficulty for a valid PoW block
var powDifficultyBig = big.NewInt(int64(powDifficulty))

// sigToDifficulty computes integer from first bytes (in little endian) and computes difficulty
// out of it.
func sigToDifficulty(b []byte) uint64 {
	var x uint64
	if len(b) > 0 {
		x = uint64(b[0])
		if len(b) > 1 {
			x |= (uint64(b[1]) << 8)
		}
	}
	return x + minDifficulty
}

// extracts the Ethereum account address from a signed header.
func ecrecoverHeader(header *types.Header, sigcache *lru.ARCCache) (common.Address, error) {
	sealB, err := SealPoKWBytes(header)
	if err != nil {
		return common.Address{}, err
	}
	var id common.Hash
	var sealHashed = make([]byte, 32)
	hr := makeHasher(sha3.NewLegacyKeccak256())

	fmt.Fprintln(os.Stderr, "BEFORE  ",sealHashed )
	hr( id[:],append(sealB, header.Sig...))
	hr(sealHashed,sealB)
	
	fmt.Fprintln(os.Stderr, "AFTER  ", sealHashed)
	// If the signature's already cached, return that
	// id := header.Hash()
	if address, known := sigcache.Get(id); known {
		return address.(common.Address), nil
	}
	// Retrieve the signature from the header extra-data
	if len(header.Sig) == 0 {
		return common.Address{}, errMissingSignature
	}

	addr, err := ecrecover(sealHashed, header.Sig)
	if err != nil {
		return addr, fmt.Errorf("Can't recover address from header signature [%w]", err)
	}
	fmt.Fprintln(os.Stderr, "--ADDR ", addr.Hex())
	sigcache.Add(id, addr)
	return addr, err
}

// Recover the public key and the Ethereum address
// crypto.Ecrecover doesn't transform / hash a message, so here we need to hash it accordingly
// to the hash used in by the wallet (c.signFn)
// sic! there is no consistency - we need to know upfront the hashing algorithm!
func ecrecover(msg, sig []byte) (common.Address, error) {
	pubkey, err := crypto.Ecrecover(msg, sig)
	if err != nil {
		return common.Address{}, err
	}

	var signer common.Address
	copy(signer[:], crypto.Keccak256(pubkey[1:])[12:])
	return signer, nil

}

func bigUint(x uint64) *big.Int {
	return new(big.Int).SetUint64(x)
}