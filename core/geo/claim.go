package geo

import (
	"bytes"
	"geo-observers-blockchain/core/common"
	"geo-observers-blockchain/core/common/errors"
	"geo-observers-blockchain/core/common/types/transactions"
	"geo-observers-blockchain/core/utils"
	"sort"
)

var (
	ClaimMinBinarySize = transactions.TxIDBinarySize + ClaimMembersMinBinarySize
)

type Claim struct {
	TxUUID  *transactions.TxID
	Members *ClaimMembers
}

func NewClaim() *Claim {
	return &Claim{
		TxUUID:  transactions.NewEmptyTxID(),
		Members: &ClaimMembers{},
	}
}

func (claim *Claim) MarshalBinary() (data []byte, err error) {
	if claim.TxUUID == nil || claim.Members == nil {
		return nil, errors.NilInternalDataStructure
	}

	txIDBinary, err := claim.TxID().MarshalBinary()
	if err != nil {
		return
	}

	membersBinary, err := claim.Members.MarshalBinary()
	if err != nil {
		return
	}

	data = utils.ChainByteSlices(txIDBinary, membersBinary)
	return
}

func (claim *Claim) UnmarshalBinary(data []byte) (err error) {
	if len(data) < ClaimMinBinarySize {
		return errors.InvalidDataFormat
	}

	const (
		offsetUUIDData    = 0
		offsetMembersData = offsetUUIDData + transactions.TxIDBinarySize
	)

	claim.TxUUID = transactions.NewEmptyTxID()
	err = claim.TxUUID.UnmarshalBinary(data[:transactions.TxIDBinarySize])
	if err != nil {
		return
	}

	claim.Members = &ClaimMembers{}
	err = claim.Members.UnmarshalBinary(data[offsetMembersData:])
	if err != nil {
		return
	}

	return
}

func (claim *Claim) TxID() *transactions.TxID {
	return claim.TxUUID
}

// --------------------------------------------------------------------------------------------------------------------

const (
	ClaimsMaxCount = 1024 * 16
)

type Claims struct {
	At []*Claim
}

func (c *Claims) Add(claim *Claim) error {
	if claim == nil {
		return errors.NilParameter
	}

	if c.Count() < ClaimsMaxCount {
		c.At = append(c.At, claim)
		return nil
	}

	return errors.MaxCountReached
}

func (c *Claims) Count() uint16 {
	return uint16(len(c.At))
}

// todo: tests needed
func (c *Claims) Sort() (err error) {
	defer func() {
		if r := recover(); r != nil {
			err = r.(error)
			return
		}
	}()

	sort.Slice(c.At, func(i, j int) bool {
		aBinaryData, err := c.At[i].MarshalBinary()
		if err != nil {
			panic(err)
		}

		bBinaryData, err := c.At[j].MarshalBinary()
		if err != nil {
			panic(err)
		}

		return bytes.Compare(aBinaryData, bBinaryData) == -1
	})

	return
}

// Format:
// 2B - Total claims count.
// [4B, 4B, ... 4B] - ClaimsHashes sizes.
// [NB, NB, ... NB] - ClaimsHashes bodies.
func (c *Claims) MarshalBinary() (data []byte, err error) {
	var (
		initialDataSize = common.Uint16ByteSize + // Total claims count.
			common.Uint16ByteSize*c.Count() // ClaimsHashes sizes fields.
	)

	data = make([]byte, 0, initialDataSize)
	data = append(data, utils.MarshalUint16(c.Count())...)
	claims := make([][]byte, 0, c.Count())

	for _, claim := range c.At {
		claimBinary, err := claim.MarshalBinary()
		if err != nil {
			return nil, err
		}

		// Skip empty claim, if any.
		if len(claimBinary) == 0 {
			continue
		}

		// Append claim size directly to the data stream.
		data = append(data, utils.MarshalUint32(uint32(len(claimBinary)))...)

		// ClaimsHashes would be attached to the data after all claims size fields would be written.
		claims = append(claims, claimBinary)
	}

	data = append(data, utils.ChainByteSlices(claims...)...)
	return
}

func (c *Claims) UnmarshalBinary(data []byte) (err error) {
	count, err := utils.UnmarshalUint16(data[:common.Uint16ByteSize])
	if err != nil {
		return
	}

	c.At = make([]*Claim, count, count)
	if count == 0 {
		return
	}

	var i uint16
	for i = 0; i < count; i++ {
		c.At[i] = NewClaim()
	}

	claimsSizes := make([]uint32, 0, count)

	var offset uint32 = common.Uint16ByteSize
	for i = 0; i < count; i++ {
		claimSize, err := utils.UnmarshalUint32(data[offset : offset+common.Uint32ByteSize])
		if err != nil {
			return err
		}
		if claimSize == 0 {
			err = errors.InvalidDataFormat
		}

		claimsSizes = append(claimsSizes, claimSize)
		offset += common.Uint32ByteSize
	}

	for i = 0; i < count; i++ {
		claim := NewClaim()
		claimSize := claimsSizes[i]

		err = claim.UnmarshalBinary(data[offset : offset+claimSize])
		if err != nil {
			return err
		}

		offset += claimSize
		c.At[i] = claim
	}

	return
}
