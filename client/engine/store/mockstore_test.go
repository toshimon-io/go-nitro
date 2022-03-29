package store_test

import (
	"testing"

	"github.com/ethereum/go-ethereum/common"
	"github.com/google/go-cmp/cmp"
	"github.com/statechannels/go-nitro/channel/consensus_channel"
	cc "github.com/statechannels/go-nitro/channel/consensus_channel"
	"github.com/statechannels/go-nitro/channel/state"
	"github.com/statechannels/go-nitro/client/engine/store"
	nc "github.com/statechannels/go-nitro/crypto"
	td "github.com/statechannels/go-nitro/internal/testdata"
	"github.com/statechannels/go-nitro/protocols"
)

func TestNewMockStore(t *testing.T) {
	sk := common.Hex2Bytes(`2af069c584758f9ec47c4224a8becc1983f28acfbe837bd7710b70f9fc6d5e44`)
	store.NewMockStore(sk)
}

func TestSetGetObjective(t *testing.T) {
	sk := common.Hex2Bytes(`2af069c584758f9ec47c4224a8becc1983f28acfbe837bd7710b70f9fc6d5e44`)

	ms := store.NewMockStore(sk)

	id := protocols.ObjectiveId("404")
	got, err := ms.GetObjectiveById(id)
	if err == nil {
		t.Fatalf("expected not to find the %s objective, but found %v", id, got)
	}

	wants := []protocols.Objective{}
	dfo := td.Objectives.Directfund.GenericDFO()
	vfo := td.Objectives.Virtualfund.GenericVFO()
	wants = append(wants, &dfo)
	wants = append(wants, &vfo)

	for _, want := range wants {

		if err := ms.SetObjective(want); err != nil {
			t.Errorf("error setting objective %v: %s", want, err.Error())
		}

		got, err = ms.GetObjectiveById(want.Id())

		if err != nil {
			t.Errorf("expected to find the inserted objective, but didn't: %s", err)
		}

		if got.Id() != want.Id() {
			t.Errorf("expected to retrieve same objective Id as was passed in, but didn't")
		}

		if diff := cmp.Diff(got, want); diff != "" {
			t.Errorf("expected no diff between set and retrieved objective, but found:\n%s", diff)
		}
	}
}

func TestGetObjectiveByChannelId(t *testing.T) {
	sk := common.Hex2Bytes(`2af069c584758f9ec47c4224a8becc1983f28acfbe837bd7710b70f9fc6d5e44`)

	ms := store.NewMockStore(sk)

	wants := []protocols.Objective{}
	dfo := td.Objectives.Directfund.GenericDFO()
	vfo := td.Objectives.Virtualfund.GenericVFO()
	wants = append(wants, &dfo)
	wants = append(wants, &vfo)

	for _, want := range wants {

		if err := ms.SetObjective(want); err != nil {
			t.Errorf("error setting objective %v: %s", want, err.Error())
		}

		for _, ch := range want.Channels() { // test target objective retrieval for each associated channel

			got, ok := ms.GetObjectiveByChannelId(ch.Id)

			if !ok {
				t.Errorf("expected to find the inserted objective, but didn't")
			}
			if got.Id() != want.Id() {
				t.Errorf("expected to retrieve same objective Id as was passed in, but didn't")
			}
			if diff := cmp.Diff(got, want); diff != "" {
				t.Errorf("expected no diff between set and retrieved objective, but found:\n%s", diff)
			}
		}

	}
}

func TestGetChannelSecretKey(t *testing.T) {
	// from state/test-fixtures.go
	sk := common.Hex2Bytes("caab404f975b4620747174a75f08d98b4e5a7053b691b41bcfc0d839d48b7634")
	pk := common.HexToAddress("0xF5A1BB5607C9D079E46d1B3Dc33f257d937b43BD")

	ms := store.NewMockStore(sk)
	key := ms.GetChannelSecretKey()

	msg := []byte("sign this")

	signedMsg, _ := nc.SignEthereumMessage(msg, *key)
	recoveredSigner, _ := nc.RecoverEthereumMessageSigner(msg, signedMsg)

	if recoveredSigner != pk {
		t.Fatalf("expected to recover %x, but got %x", pk, recoveredSigner)
	}
}

func TestConsensusChannelStore(t *testing.T) {
	sk := common.Hex2Bytes(`2af069c584758f9ec47c4224a8becc1983f28acfbe837bd7710b70f9fc6d5e44`)

	ms := store.NewMockStore(sk)

	got, ok := ms.GetConsensusChannel(td.Actors.Alice.Address, td.Actors.Bob.Address)
	if ok {
		t.Fatalf("expected not to find the a consensus channel, but found %v", got)
	}

	fp := td.Objectives.Directfund.GenericDFO().C.FixedPart
	fp.Participants[0] = td.Actors.Alice.Address
	fp.Participants[1] = td.Actors.Bob.Address
	initialVars := consensus_channel.Vars{Outcome: cc.LedgerOutcome{}, TurnNum: 0}
	aliceSig, _ := initialVars.AsState(fp).Sign(td.Actors.Alice.PrivateKey)
	bobsSig, _ := initialVars.AsState(fp).Sign(td.Actors.Bob.PrivateKey)

	want, err := consensus_channel.NewLeaderChannel(
		fp,
		consensus_channel.LedgerOutcome{},
		[2]state.Signature{aliceSig, bobsSig})

	if err != nil {
		t.Fatal(err)
	}

	if err := ms.SetConsensusChannel(&want.ConsensusChannel); err != nil {
		t.Fatalf("error setting consensus channel %v: %s", want, err.Error())
	}

	got, ok = ms.GetConsensusChannel(fp.Participants[0], fp.Participants[1])

	if !ok {
		t.Fatalf("expected to find the inserted consensus channel, but didn't")
	}

	if got.Id != want.Id {
		t.Fatalf("expected to retrieve same channel Id as was passed in, but didn't")
	}
	// TODO check that got and want are deeply equal
}
