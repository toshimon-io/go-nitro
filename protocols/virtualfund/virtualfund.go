// Package virtualfund implements an off-chain protocol to virtually fund a channel.
package virtualfund // import "github.com/statechannels/go-nitro/virtualfund"

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"math/big"
	"reflect"
	"strings"

	"github.com/statechannels/go-nitro/channel"
	"github.com/statechannels/go-nitro/channel/state"
	"github.com/statechannels/go-nitro/channel/state/outcome"
	"github.com/statechannels/go-nitro/crypto"

	"github.com/statechannels/go-nitro/protocols"
	"github.com/statechannels/go-nitro/types"
)

const (
	WaitingForCompletePrefund  protocols.WaitingFor = "WaitingForCompletePrefund"  // Round 1
	WaitingForCompleteFunding  protocols.WaitingFor = "WaitingForCompleteFunding"  // Round 2
	WaitingForCompletePostFund protocols.WaitingFor = "WaitingForCompletePostFund" // Round 3
	WaitingForNothing          protocols.WaitingFor = "WaitingForNothing"          // Finished
)

const ObjectivePrefix = "VirtualFund-"

// errors
var ErrNotApproved = errors.New("objective not approved")

// GuaranteeInfo contains the information used to generate the expected guarantees.
type GuaranteeInfo struct {
	Left                 types.Destination
	Right                types.Destination
	LeftAmount           types.Funds
	RightAmount          types.Funds
	GuaranteeDestination types.Destination
}
type Connection struct {
	Channel            *channel.TwoPartyLedger
	ExpectedGuarantees map[types.Address]outcome.Allocation
	GuaranteeInfo      GuaranteeInfo
}

// Equal returns true if the Connection pointed to by the supplied pointer is deeply equal to the receiver.
func (c *Connection) Equal(d *Connection) bool {
	if c == nil && d == nil {
		return true
	}
	if !c.Channel.Equal(d.Channel) {
		return false
	}
	if !reflect.DeepEqual(c.ExpectedGuarantees, d.ExpectedGuarantees) {
		return false
	}
	if !reflect.DeepEqual(c.GuaranteeInfo, d.GuaranteeInfo) {
		return false
	}
	return true

}

// jsonConnection is a serialization-friendly struct representation
// of a Connection
type jsonConnection struct {
	Channel            types.Destination
	ExpectedGuarantees []assetGuarantee
	GuarnateeInfo      GuaranteeInfo
}

// MarshalJSON returns a JSON representation of the Connection
//
// NOTE: Marshal -> Unmarshal is a lossy process. All channel data
//       other than the ID is dropped
func (c Connection) MarshalJSON() ([]byte, error) {
	guarantees := []assetGuarantee{}
	for asset, guarantee := range c.ExpectedGuarantees {
		guarantees = append(guarantees, assetGuarantee{
			asset,
			guarantee,
		})
	}
	jsonC := jsonConnection{c.Channel.Id, guarantees, c.GuaranteeInfo}
	bytes, err := json.Marshal(jsonC)

	if err != nil {
		return []byte{}, err
	}

	return bytes, err
}

// UnmarshalJSON populates the calling Connection with the
// json-encoded data
//
// NOTE: Marshal -> Unmarshal is a lossy process. All channel data from
//       (other than Id) is discarded
func (c *Connection) UnmarshalJSON(data []byte) error {
	c.Channel = &channel.TwoPartyLedger{}
	c.ExpectedGuarantees = make(map[types.Address]outcome.Allocation)

	if string(data) == "null" {
		// populate a well-formed but blank-addressed Connection
		c.Channel.Id = types.Destination{}
		return nil
	}

	var jsonC jsonConnection
	err := json.Unmarshal(data, &jsonC)

	if err != nil {
		return err
	}

	c.Channel.Id = jsonC.Channel
	c.GuaranteeInfo = jsonC.GuarnateeInfo

	for _, eg := range jsonC.ExpectedGuarantees {
		c.ExpectedGuarantees[eg.Asset] = eg.Guarantee
	}

	return nil
}

// assetGuarantee is a serialization-friendly representation of
// map[asset]Allocation
type assetGuarantee struct {
	Asset     types.Address
	Guarantee outcome.Allocation
}

// Objective is a cache of data computed by reading from the store. It stores (potentially) infinite data.
type Objective struct {
	Status protocols.ObjectiveStatus
	StartingState state.SignedState
	postFundSigs  map[uint]state.Signature // keyed by participant index
	targetId types.Destination
	// V      *channel.SingleHopVirtualChannel

	ToMyLeft  *Connection
	ToMyRight *Connection

	n      uint // number of intermediaries
	MyRole uint // index in the virtual funding protocol. 0 for Alice, n for Bob. Otherwise, one of the intermediaries.

	a0 types.Funds // Initial balance for Alice
	b0 types.Funds // Initial balance for Bob

}

// jsonObjective replaces the virtualfund Objective's channel pointers
// with the channel's respective IDs, making jsonObjective suitable for serialization
type jsonObjective struct {
	Status protocols.ObjectiveStatus
	// V      types.Destination

	ToMyLeft  []byte
	ToMyRight []byte

	N      uint
	MyRole uint

	A0 types.Funds
	B0 types.Funds
}

// NewObjective creates a new virtual funding objective from a given request.
func NewObjective(request ObjectiveRequest, getTwoPartyLedger GetTwoPartyLedgerFunction) (Objective, error) {
	right, ok := getTwoPartyLedger(request.MyAddress, request.Intermediary)

	if !ok {
		return Objective{}, fmt.Errorf("could not find ledger for %s and %s", request.MyAddress, request.Intermediary)

	}
	var left *channel.TwoPartyLedger

	objective, err := constructFromState(true,
		state.State{
			ChainId:           big.NewInt(0), // TODO
			Participants:      []types.Address{request.MyAddress, request.Intermediary, request.CounterParty},
			ChannelNonce:      big.NewInt(request.Nonce),
			ChallengeDuration: request.ChallengeDuration,
			AppData:           request.AppData,
			Outcome:           request.Outcome,
			TurnNum:           0,
			IsFinal:           false,
		},
		request.MyAddress,
		left, right)
	if err != nil {
		return Objective{}, fmt.Errorf("error creating objective: %w", err)
	}
	return objective, nil

}

// constructFromState initiates an Objective from an initial state and set of ledgers.
func constructFromState(
	preApprove bool,
	initialStateOfV state.State,
	myAddress types.Address,
	ledgerChannelToMyLeft *channel.TwoPartyLedger,
	ledgerChannelToMyRight *channel.TwoPartyLedger,
) (Objective, error) {

	var init Objective

	if preApprove {
		init.Status = protocols.Approved
	} else {
		init.Status = protocols.Unapproved
	}

	// Infer MyRole

	err := validateInitialState(initialStateOfV)
	if err != nil {
		return Objective{}, err
	}

	found := false
	for i, addr := range initialStateOfV.Participants {
		if bytes.Equal(addr[:], myAddress[:]) {
			init.MyRole = uint(i)
			found = true
			continue
		}
	}
	if !found {
		return Objective{}, errors.New("not a participant in V")
	}

	participants := initialStateOfV.Participants
	leftOfMe, rightOfMe := types.Destination{}, types.Destination{}
	if !init.isAlice() {
		leftOfMe = types.AddressToDestination(participants[init.MyRole-1])
	}
	me := types.AddressToDestination(participants[init.MyRole])
	if !init.isBob() {
		rightOfMe = types.AddressToDestination(participants[init.MyRole+1])
	}
	vId, err := initialStateOfV.ChannelId()
	if err != nil {
		return Objective{}, err
	}
	init.targetId = vId

	init.StartingState = state.NewSignedState(initialStateOfV)
	init.postFundSigs = make(map[uint]crypto.Signature)

	init.n = uint(len(initialStateOfV.Participants)) - 2 // NewSingleHopVirtualChannel will error unless there are at least 3 participants

	init.a0 = make(map[types.Address]*big.Int)
	init.b0 = make(map[types.Address]*big.Int)

	// Compute a0 and b0 from the initial state of J
	for i := range initialStateOfV.Outcome {
		asset := initialStateOfV.Outcome[i].Asset
		amount0 := initialStateOfV.Outcome[i].Allocations[0].Amount
		amount1 := initialStateOfV.Outcome[i].Allocations[1].Amount
		if init.a0[asset] == nil {
			init.a0[asset] = big.NewInt(0)
		}
		if init.b0[asset] == nil {
			init.b0[asset] = big.NewInt(0)
		}
		init.a0[asset].Add(init.a0[asset], amount0)
		init.b0[asset].Add(init.b0[asset], amount1)
	}

	// Setup Ledger Channel Connections and expected guarantees
	if !init.isAlice() { // everyone other than Alice has a left-channel
		init.ToMyLeft = &Connection{}
		init.ToMyLeft.Channel = ledgerChannelToMyLeft
		err := init.ToMyLeft.insertExpectedGuarantees( init.a0, init.b0, init.targetId, leftOfMe, me,)
		if err != nil {
			return Objective{}, err
		}
	}

	if !init.isBob() { // everyone other than Bob has a right-channel
		init.ToMyRight = &Connection{}
		init.ToMyRight.Channel = ledgerChannelToMyRight
		err := init.ToMyRight.insertExpectedGuarantees( init.a0, init.b0, init.targetId, me, rightOfMe)
		if err != nil {
			return Objective{}, err
		}
	}

	return init, nil
}

// Id returns the objective id.
func (o Objective) Id() protocols.ObjectiveId {
	return protocols.ObjectiveId(ObjectivePrefix + o.targetId.String())
}

// Approve returns an approved copy of the objective.
func (o Objective) Approve() protocols.Objective {
	updated := o.clone()
	// todo: consider case of s.Status == Rejected
	updated.Status = protocols.Approved
	return &updated
}

// Approve returns a rejected copy of the objective.
func (o Objective) Reject() protocols.Objective {
	updated := o.clone()
	updated.Status = protocols.Rejected
	return &updated
}

// Update receives an protocols.ObjectiveEvent, applies all applicable event data to the VirtualFundObjective,
// and returns the updated state.
func (o Objective) Update(event protocols.ObjectiveEvent) (protocols.Objective, error) {
	err := error(nil)

	if o.Id() != event.ObjectiveId {
		return &o, fmt.Errorf("event and objective Ids do not match: %s and %s respectively", string(event.ObjectiveId), string(o.Id()))
	}

	updated := o.clone()

	var toMyLeftId types.Destination
	var toMyRightId types.Destination

	if !o.isAlice() {
		toMyLeftId = o.ToMyLeft.Channel.Id // Avoid this if it is nil
	}
	if !o.isBob() {
		toMyRightId = o.ToMyRight.Channel.Id // Avoid this if it is nil
	}

	for _, ss := range event.SignedStates {
		channelId, _ := ss.State().ChannelId() // TODO handle error
		switch channelId {
		case types.Destination{}:
			return &o, errors.New("null channel id") // catch this case to avoid a panic below -- because if Alice or Bob we allow a null channel.
		case o.targetId:
			if ss.State().TurnNum == 0 {
				err = updated.StartingState.Merge(ss)
			} else if ss.State().TurnNum == 1 {
				postFS := updated.PostFundSetup()
				err = postFS.Merge(ss)
				updated.postFundSigs = postFS.Signatures()
			} else {
				err = fmt.Errorf("unexpected state received")
			}

			if err != nil {
				return &Objective{}, nil
			}

		case toMyLeftId:
			updated.ToMyLeft.Channel.AddSignedState(ss)
			// We expect a countersigned state including an outcome with expected guarantee. We don't know the exact statehash, though.
		case toMyRightId:
			updated.ToMyRight.Channel.AddSignedState(ss)
			// We expect a countersigned state including an outcome with expected guarantee. We don't know the exact statehash, though.
		default:
			return &o, errors.New("event channelId out of scope of objective")
		}
	}
	return &updated, nil

}

// Crank inspects the extended state and declares a list of Effects to be executed
// It's like a state machine transition function where the finite / enumerable state is returned (computed from the extended state)
// rather than being independent of the extended state; and where there is only one type of event ("the crank") with no data on it at all.
func (o Objective) Crank(secretKey *[]byte) (protocols.Objective, protocols.SideEffects, protocols.WaitingFor, error) {
	updated := o.clone()

	sideEffects := protocols.SideEffects{}
	// Input validation
	if updated.Status != protocols.Approved {
		return &updated, sideEffects, WaitingForNothing, ErrNotApproved
	}

	// Prefunding

	preFundSignedByMe := updated.StartingState.HasSignatureForParticipant(updated.MyRole)
	if !preFundSignedByMe {
		err := updated.StartingState.Sign(secretKey)
		if err != nil {
			return &o, protocols.SideEffects{}, WaitingForNothing, err
		}
		messages := protocols.CreateSignedStateMessages(o.Id(), updated.StartingState, o.MyRole)
		sideEffects.MessagesToSend = append(sideEffects.MessagesToSend, messages...)
	}

	preFundComplete := updated.StartingState.HasAllSignatures()
	if !preFundComplete {
		return &updated, sideEffects, WaitingForCompletePrefund, nil
	}

	// Funding

	if !updated.isAlice() && !updated.ToMyLeft.ledgerChannelAffordsExpectedGuarantees() {

		ledgerSideEffects, err := updated.updateLedgerWithGuarantee(*updated.ToMyLeft, secretKey)
		if err != nil {
			return &o, protocols.SideEffects{}, WaitingForNothing, fmt.Errorf("error updating ledger funding: %w", err)
		}
		sideEffects.Merge(ledgerSideEffects)
	}

	if !updated.isBob() && !updated.ToMyRight.ledgerChannelAffordsExpectedGuarantees() {
		ledgerSideEffects, err := updated.updateLedgerWithGuarantee(*updated.ToMyRight, secretKey)
		if err != nil {
			return &o, protocols.SideEffects{}, WaitingForNothing, fmt.Errorf("error updating ledger funding: %w", err)
		}
		sideEffects.Merge(ledgerSideEffects)
	}

	if !updated.fundingComplete() {
		return &updated, sideEffects, WaitingForCompleteFunding, nil
	}

	// Postfunding
	_, postFundSignedByMe := updated.postFundSigs[updated.MyRole]
	
	if !postFundSignedByMe {
		s := updated.PostFundSetup()
		err := s.Sign(secretKey)
		if err != nil {
			return &o, protocols.SideEffects{}, WaitingForNothing, err
		}
		messages := protocols.CreateSignedStateMessages(o.Id(), s, o.MyRole)
		sideEffects.MessagesToSend = append(sideEffects.MessagesToSend, messages...)
	}

	postFundComplete := updated.PostFundSetup().HasAllSignatures()
	if !postFundComplete {
		return &updated, sideEffects, WaitingForCompletePostFund, nil
	}

	// Completion
	return &updated, sideEffects, WaitingForNothing, nil
}

func (o Objective) PostFundSetup() state.SignedState {
	s := o.StartingState.Clone().State()
	sigs := o.postFundSigs
	return state.NewSignedStateWithSigs(s, sigs)
}

func (o Objective) Channels() []*channel.Channel {
	ret := make([]*channel.Channel, 0, 3)
	if !o.isAlice() {
		ret = append(ret, &o.ToMyLeft.Channel.Channel)
	}
	if !o.isBob() {
		ret = append(ret, &o.ToMyRight.Channel.Channel)
	}
	return ret
}

// MarshalJSON returns a JSON representation of the VirtualFundObjective
//
// NOTE: Marshal -> Unmarshal is a lossy process. All channel data from
//       the virtual and ledger channels (other than Ids) is discarded
func (o Objective) MarshalJSON() ([]byte, error) {
	var left []byte
	var right []byte
	var err error

	if o.ToMyLeft == nil {
		left = []byte("null")
	} else {
		left, err = o.ToMyLeft.MarshalJSON()

		if err != nil {
			return nil, fmt.Errorf("error marshaling left channel of %v: %w", o, err)
		}
	}

	if o.ToMyRight == nil {
		right = []byte("null")
	} else {
		right, err = o.ToMyRight.MarshalJSON()

		if err != nil {
			return nil, fmt.Errorf("error marshaling right channel of %v: %w", o, err)
		}
	}

	jsonVFO := jsonObjective{
		o.Status,
		// o.V.Id,
		left,
		right,
		o.n,
		o.MyRole,
		o.a0,
		o.b0,
	}
	return json.Marshal(jsonVFO)
}

// UnmarshalJSON populates the calling VirtualFundObjective with the
// json-encoded data
//
// NOTE: Marshal -> Unmarshal is a lossy process. All channel data from
//       the virtual and ledger channels (other than Ids) is discarded
func (o *Objective) UnmarshalJSON(data []byte) error {
	if string(data) == "null" {
		return nil
	}

	var jsonVFO jsonObjective
	if err := json.Unmarshal(data, &jsonVFO); err != nil {
		return fmt.Errorf("failed to unmarshal the VirtualFundObjective: %w", err)
	}

	// o.V = &channel.SingleHopVirtualChannel{}
	// o.V.Id = jsonVFO.V

	o.ToMyLeft = &Connection{}
	o.ToMyRight = &Connection{}
	if err := o.ToMyLeft.UnmarshalJSON(jsonVFO.ToMyLeft); err != nil {
		return fmt.Errorf("failed to unmarshal left ledger channel: %w", err)
	}
	if err := o.ToMyRight.UnmarshalJSON(jsonVFO.ToMyRight); err != nil {
		return fmt.Errorf("failed to unmarshal right ledger channel: %w", err)
	}

	// connection.Unmarshal cannot populate a nil connection, so instead returns
	// a non-connection to a channel with the zero-Id. virtualfund.objective expects
	// these connection objects to be nil.
	zeroAddress := types.Destination{}
	if o.ToMyLeft.Channel.Id == zeroAddress {
		o.ToMyLeft = nil
	}
	if o.ToMyRight.Channel.Id == zeroAddress {
		o.ToMyRight = nil
	}

	o.Status = jsonVFO.Status
	o.n = jsonVFO.N
	o.MyRole = jsonVFO.MyRole
	o.a0 = jsonVFO.A0
	o.b0 = jsonVFO.B0

	return nil
}

//////////////////////////////////////////////////
//  Private methods on the VirtualFundObjective //
//////////////////////////////////////////////////

// insertExpectedGuaranteesForLedgerChannel mutates the reciever Connection struct.
func (connection *Connection) insertExpectedGuarantees(a0 types.Funds, b0 types.Funds, vId types.Destination, left types.Destination, right types.Destination) error {
	expectedGuaranteesForLedgerChannel := make(map[types.Address]outcome.Allocation)
	metadata := outcome.GuaranteeMetadata{
		Left:  left,
		Right: right,
	}
	encodedGuarantee, err := metadata.Encode()
	if err != nil {
		return err
	}

	channelFunds := a0.Add(b0)

	for asset, amount := range channelFunds {
		expectedGuaranteesForLedgerChannel[asset] = outcome.Allocation{
			Destination:    vId,
			Amount:         amount,
			AllocationType: outcome.GuaranteeAllocationType,
			Metadata:       encodedGuarantee,
		}
	}
	connection.ExpectedGuarantees = expectedGuaranteesForLedgerChannel

	connection.GuaranteeInfo = GuaranteeInfo{
		Left:                 left,
		Right:                right,
		LeftAmount:           a0,
		RightAmount:          b0,
		GuaranteeDestination: vId,
	}

	return nil
}

// fundingComplete returns true if the appropriate ledger channel guarantees sufficient funds for J
func (o Objective) fundingComplete() bool {

	// Each peer commits to an update in L_{i-1} and L_i including the guarantees G_{i-1} and {G_i} respectively, and deducting b_0 from L_{I-1} and a_0 from L_i.
	// A = P_0 and B=P_n are special cases. A only does the guarantee for L_0 (deducting a0), and B only foes the guarantee for L_n (deducting b0).

	switch {
	case o.isAlice(): // Alice
		return o.ToMyRight.ledgerChannelAffordsExpectedGuarantees()
	default: // Intermediary
		return o.ToMyRight.ledgerChannelAffordsExpectedGuarantees() && o.ToMyLeft.ledgerChannelAffordsExpectedGuarantees()
	case o.isBob(): // Bob
		return o.ToMyLeft.ledgerChannelAffordsExpectedGuarantees()
	}

}

// ledgerChannelAffordsExpectedGuarantees returns true if, for the channel inside the connection, and for each asset keying the input variables, the channel can afford the allocation given the funding.
// The decision is made based on the latest supported state of the channel.
//
// Both arguments are maps keyed by the same asset.
func (connection *Connection) ledgerChannelAffordsExpectedGuarantees() bool {
	return connection.Channel.Affords(connection.ExpectedGuarantees, connection.Channel.OnChainFunding)
}

// Equal returns true if the supplied DirectFundObjective is deeply equal to the receiver.
func (o Objective) Equal(r Objective) bool {
	return o.Status == r.Status &&
		// o.V.Equal(r.V) &&
		o.ToMyLeft.Equal(r.ToMyLeft) &&
		o.ToMyRight.Equal(r.ToMyRight) &&
		o.n == r.n &&
		o.MyRole == r.MyRole &&
		o.a0.Equal(r.a0) &&
		o.b0.Equal(r.b0)
}

// Clone returns a deep copy of the receiver.
func (o *Objective) clone() Objective {
	clone := Objective{}
	clone.Status = o.Status
	// vClone := o.V.Clone()
	// clone.V = vClone

	if o.ToMyLeft != nil {
		lClone := o.ToMyLeft.Channel.Clone()
		clone.ToMyLeft = &Connection{
			Channel:            lClone,
			ExpectedGuarantees: o.ToMyLeft.ExpectedGuarantees,
			GuaranteeInfo:      o.ToMyLeft.GuaranteeInfo,
		}
	}

	if o.ToMyRight != nil {
		rClone := o.ToMyRight.Channel.Clone()
		clone.ToMyRight = &Connection{
			Channel:            rClone,
			ExpectedGuarantees: o.ToMyRight.ExpectedGuarantees,
			GuaranteeInfo:      o.ToMyRight.GuaranteeInfo,
		}
	}

	clone.n = o.n
	clone.MyRole = o.MyRole

	clone.a0 = o.a0
	clone.b0 = o.b0
	return clone
}

// isAlice returns true if the reciever represents participant 0 in the virtualfund protocol.
func (o *Objective) isAlice() bool {
	return o.MyRole == 0
}

// isBob returns true if the reciever represents participant n+1 in the virtualfund protocol.
func (o *Objective) isBob() bool {
	return o.MyRole == o.n+1
}

// GetTwoPartyLedgerFunction specifies a function that can be used to retreive ledgers from a store.
type GetTwoPartyLedgerFunction func(firstParty types.Address, secondParty types.Address) (ledger *channel.TwoPartyLedger, ok bool)

// ConstructObjectiveFromMessage takes in a message and constructs an objective from it.
// It accepts the message, myAddress, and a function to to retrieve ledgers from a store.
func ConstructObjectiveFromMessage(m protocols.Message, myAddress types.Address, getTwoPartyLedger GetTwoPartyLedgerFunction) (Objective, error) {
	if len(m.SignedStates) != 1 {
		return Objective{}, errors.New("expected exactly one signed state in the message")
	}

	initialState := m.SignedStates[0].State()
	participants := initialState.Participants

	// This logic assumes a single hop virtual channel.
	// Currently this is the only type of virtual channel supported.
	alice := participants[0]
	intermediary := participants[1]
	bob := participants[2]

	var left *channel.TwoPartyLedger
	var right *channel.TwoPartyLedger
	var ok bool

	if myAddress == alice {
		return Objective{}, errors.New("participant[0] should not construct objectives from peer messages")
	} else if myAddress == bob {
		left, ok = getTwoPartyLedger(intermediary, bob)
		if !ok {
			return Objective{}, fmt.Errorf("could not find a left ledger channel between %v and %v", intermediary, bob)
		}
	} else if myAddress == intermediary {
		left, ok = getTwoPartyLedger(alice, intermediary)
		if !ok {
			return Objective{}, fmt.Errorf("could not find a left ledger channel between %v and %v", alice, intermediary)
		}
		right, ok = getTwoPartyLedger(intermediary, bob)
		if !ok {
			return Objective{}, fmt.Errorf("could not find a right ledger channel between %v and %v", intermediary, bob)
		}
	} else {
		return Objective{}, fmt.Errorf("client address not found in an expected participant index")
	}

	return constructFromState(
		true, // TODO ensure objective in only approved if the application has given permission somehow
		initialState,
		myAddress,
		left,
		right,
	)
}

// IsVirtualFundObjective inspects a objective id and returns true if the objective id is for a virtual fund objective.
func IsVirtualFundObjective(id protocols.ObjectiveId) bool {
	return strings.HasPrefix(string(id), ObjectivePrefix)
}

// proposeLedgerUpdate will propose a ledger update to the channel by crafting a new state
func (o *Objective) proposeLedgerUpdate(connection Connection, sk *[]byte) (protocols.SideEffects, error) {
	ledger := connection.Channel
	left := connection.GuaranteeInfo.Left
	right := connection.GuaranteeInfo.Right
	leftAmount := connection.GuaranteeInfo.LeftAmount
	rightAmount := connection.GuaranteeInfo.RightAmount

	if !ledger.IsProposer() {
		return protocols.SideEffects{}, errors.New("only the proposer can propose a ledger update")
	}

	sideEffects := protocols.SideEffects{}

	supported, err := ledger.LatestSupportedState()
	if err != nil {
		return protocols.SideEffects{}, fmt.Errorf("error finding a supported state: %w", err)
	}

	proposed, proposedFound := ledger.Proposed()

	// Clone the state and update the turn to the next turn.
	// We want to use the most recent proposal if it exists, otherwise we use the support.
	var nextState state.State
	if proposedFound {
		nextState = proposed.Clone()
	} else {
		nextState = supported.Clone()
	}
	if nextState.Outcome.Affords(connection.ExpectedGuarantees, ledger.OnChainFunding) {
		return protocols.SideEffects{}, nil
	}
	nextState.TurnNum = nextState.TurnNum + 1
	// Update the outcome with the guarantee.
	nextState.Outcome, err = nextState.Outcome.DivertToGuarantee(left, right, leftAmount, rightAmount, o.targetId)
	if err != nil {
		return protocols.SideEffects{}, fmt.Errorf("error updating ledger channel outcome: %w", err)
	}

	// Sign the state and add it to the ledger.
	ss, err := ledger.SignAndAddState(nextState, sk)
	if err != nil {
		return protocols.SideEffects{}, fmt.Errorf("error adding signed state: %w", err)
	}

	// Add a message with the signed state
	messages := protocols.CreateSignedStateMessages(o.Id(), ss, ledger.MyIndex)
	sideEffects.MessagesToSend = append(sideEffects.MessagesToSend, messages...)

	return sideEffects, nil
}

// acceptLedgerUpdate checks for a ledger state proposal and accepts that proposal if it satisfies the expected guarantee.
func (o *Objective) acceptLedgerUpdate(ledgerConnection Connection, sk *[]byte) (protocols.SideEffects, error) {
	ledger := ledgerConnection.Channel
	proposed, ok := ledger.Proposed()

	if !ok {
		return protocols.SideEffects{}, fmt.Errorf("no proposed state found for ledger channel %s", ledger.Id)
	}

	supported, err := ledger.LatestSupportedState()
	if err != nil {
		return protocols.SideEffects{}, fmt.Errorf("no supported state found for ledger channel %s: %w", ledger.Id, err)
	}

	// Determine if we are left or right in the guarantee and determine our amounts.
	ourAddress := types.AddressToDestination(ledger.Participants[ledger.MyIndex])
	var ourDeposit types.Funds
	if ledger.MyIndex == 0 {
		ourDeposit = ledgerConnection.GuaranteeInfo.LeftAmount
	} else if ledger.MyIndex == 1 {
		ourDeposit = ledgerConnection.GuaranteeInfo.RightAmount
	}

	ourPreviousTotal := supported.Outcome.TotalAllocatedFor(ourAddress)
	ourNewTotal := proposed.Outcome.TotalAllocatedFor(ourAddress)

	// Our new total should just be our previous total minus our deposit.
	proposedMaintainsOurFunds := ourNewTotal.Add(ourDeposit).Equal(ourPreviousTotal)

	proposedAffordsGuarantee := proposed.Outcome.Affords(ledgerConnection.ExpectedGuarantees, ledger.OnChainFunding)

	if proposedMaintainsOurFunds && proposedAffordsGuarantee {

		ss, err := ledger.SignAndAddState(proposed, sk)
		if err != nil {
			return protocols.SideEffects{}, fmt.Errorf("error adding signed state: %w", err)
		}
		sideEffects := protocols.SideEffects{}
		messages := protocols.CreateSignedStateMessages(o.Id(), ss, ledger.MyIndex)
		sideEffects.MessagesToSend = append(sideEffects.MessagesToSend, messages...)
		return sideEffects, nil
	}

	return protocols.SideEffects{}, nil

}

// updateLedgerWithGuarantee updates the ledger channel funding to include the guarantee.
// If the user is the proposer a new ledger state will be created and signed.
// If the user is the follower then they will sign a ledger state proposal if it satisfies their expected guarantees.
func (o *Objective) updateLedgerWithGuarantee(ledgerConnection Connection, sk *[]byte) (protocols.SideEffects, error) {

	ledger := ledgerConnection.Channel

	if ledger.IsProposer() { // If the user is the proposer craft a new proposal
		sideEffects, err := o.proposeLedgerUpdate(ledgerConnection, sk)
		if err != nil {
			return protocols.SideEffects{}, fmt.Errorf("error proposing ledger update: %w", err)
		} else {
			return sideEffects, nil
		}
	} else if _, ok := ledger.Proposed(); ok { // Otherwise if there is a proposal accept it if it satisfies the guarantee
		sideEffects, err := o.acceptLedgerUpdate(ledgerConnection, sk)
		if err != nil {
			return protocols.SideEffects{}, fmt.Errorf("error proposing ledger update: %w", err)
		} else {
			return sideEffects, nil
		}
	} else {
		return protocols.SideEffects{}, nil
	}

}

// ObjectiveRequest represents a request to create a new virtual funding objective.
type ObjectiveRequest struct {
	MyAddress         types.Address
	CounterParty      types.Address
	Intermediary      types.Address
	AppDefinition     types.Address
	AppData           types.Bytes
	ChallengeDuration *types.Uint256
	Outcome           outcome.Exit
	Nonce             int64
}

// Id returns the objective id for the request.
func (r ObjectiveRequest) Id() protocols.ObjectiveId {
	fixedPart := state.FixedPart{ChainId: big.NewInt(0), // TODO
		Participants:      []types.Address{r.MyAddress, r.Intermediary, r.CounterParty},
		ChannelNonce:      big.NewInt(r.Nonce),
		ChallengeDuration: r.ChallengeDuration}

	channelId, _ := fixedPart.ChannelId()
	return protocols.ObjectiveId(ObjectivePrefix + channelId.String())
}


// validateInitialState checks that
// - the channel has three participants
// - the outcome is the expected shape for a virtual channel
func validateInitialState(s state.State) error {
	if len(s.Participants) != 3 {
		return errors.New("a single hop virtual channel must have exactly three participants")
	}
	for _, assetExit := range s.Outcome {
		if len(assetExit.Allocations) != 2 {
			return errors.New("a single hop virtual channel's initial state should only have two allocations")
		}
	}

	return nil
}
