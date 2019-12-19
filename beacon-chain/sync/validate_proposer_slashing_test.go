package sync

import (
	"bytes"
	"context"
	"crypto/rand"
	"reflect"
	"testing"
	"time"

	"github.com/gogo/protobuf/proto"
	pubsub "github.com/libp2p/go-libp2p-pubsub"
	pubsubpb "github.com/libp2p/go-libp2p-pubsub/pb"
	ethpb "github.com/prysmaticlabs/ethereumapis/eth/v1alpha1"
	"github.com/prysmaticlabs/go-ssz"
	mock "github.com/prysmaticlabs/prysm/beacon-chain/blockchain/testing"
	"github.com/prysmaticlabs/prysm/beacon-chain/core/blocks"
	"github.com/prysmaticlabs/prysm/beacon-chain/core/helpers"
	"github.com/prysmaticlabs/prysm/beacon-chain/p2p"
	p2ptest "github.com/prysmaticlabs/prysm/beacon-chain/p2p/testing"
	mockSync "github.com/prysmaticlabs/prysm/beacon-chain/sync/initial-sync/testing"
	pb "github.com/prysmaticlabs/prysm/proto/beacon/p2p/v1"
	"github.com/prysmaticlabs/prysm/shared/bls"
	"github.com/prysmaticlabs/prysm/shared/params"
)

func setupValidProposerSlashing(t *testing.T) (*ethpb.ProposerSlashing, *pb.BeaconState) {
	validators := make([]*ethpb.Validator, 100)
	for i := 0; i < len(validators); i++ {
		validators[i] = &ethpb.Validator{
			EffectiveBalance:  params.BeaconConfig().MaxEffectiveBalance,
			Slashed:           false,
			ExitEpoch:         params.BeaconConfig().FarFutureEpoch,
			WithdrawableEpoch: params.BeaconConfig().FarFutureEpoch,
			ActivationEpoch:   0,
		}
	}
	validatorBalances := make([]uint64, len(validators))
	for i := 0; i < len(validatorBalances); i++ {
		validatorBalances[i] = params.BeaconConfig().MaxEffectiveBalance
	}

	currentSlot := uint64(0)
	state := &pb.BeaconState{
		Validators: validators,
		Slot:       currentSlot,
		Balances:   validatorBalances,
		Fork: &pb.Fork{
			CurrentVersion:  params.BeaconConfig().GenesisForkVersion,
			PreviousVersion: params.BeaconConfig().GenesisForkVersion,
			Epoch:           0,
		},
		Slashings:   make([]uint64, params.BeaconConfig().EpochsPerSlashingsVector),
		RandaoMixes: make([][]byte, params.BeaconConfig().EpochsPerHistoricalVector),

		StateRoots:        make([][]byte, params.BeaconConfig().SlotsPerHistoricalRoot),
		BlockRoots:        make([][]byte, params.BeaconConfig().SlotsPerHistoricalRoot),
		LatestBlockHeader: &ethpb.BeaconBlockHeader{},
	}

	domain := helpers.Domain(
		state.Fork,
		helpers.CurrentEpoch(state),
		params.BeaconConfig().DomainBeaconProposer,
	)
	privKey := bls.RandKey()

	someRoot := [32]byte{1, 2, 3}
	someRoot2 := [32]byte{4, 5, 6}
	header1 := &ethpb.BeaconBlockHeader{
		Slot:       0,
		ParentRoot: someRoot[:],
		StateRoot:  someRoot[:],
		BodyRoot:   someRoot[:],
	}
	signingRoot, err := ssz.SigningRoot(header1)
	if err != nil {
		t.Errorf("Could not get signing root of beacon block header: %v", err)
	}
	header1.Signature = privKey.Sign(signingRoot[:], domain).Marshal()[:]

	header2 := &ethpb.BeaconBlockHeader{
		Slot:       0,
		ParentRoot: someRoot2[:],
		StateRoot:  someRoot2[:],
		BodyRoot:   someRoot2[:],
	}
	signingRoot, err = ssz.SigningRoot(header2)
	if err != nil {
		t.Errorf("Could not get signing root of beacon block header: %v", err)
	}
	header2.Signature = privKey.Sign(signingRoot[:], domain).Marshal()[:]

	slashing := &ethpb.ProposerSlashing{
		ProposerIndex: 1,
		Header_1:      header1,
		Header_2:      header2,
	}

	state.Validators[1].PublicKey = privKey.PublicKey().Marshal()[:]

	b := make([]byte, 32)
	if _, err := rand.Read(b); err != nil {
		t.Fatal(err)
	}

	return slashing, state
}

func TestValidateProposerSlashing_EncodeDecode(t *testing.T) {
	slashing, _ := setupValidProposerSlashing(t)
	enc, err := ssz.Marshal(slashing)
	if err != nil {
		t.Fatal(err)
	}
	target := &ethpb.ProposerSlashing{}
	if err := ssz.Unmarshal(enc, target); err != nil {
		t.Fatal(err)
	}
	if !proto.Equal(slashing, target) {
		t.Errorf("Wanted %v, got %v", slashing, target)
	}
}

func TestValidateProposerSlashing_ValidSlashing(t *testing.T) {
	p := p2ptest.NewTestP2P(t)
	ctx := context.Background()

	slashing, s := setupValidProposerSlashing(t)
	// TODO: Sanity check, remove this.
	if err := blocks.VerifyProposerSlashing(s, slashing); err != nil {
		t.Fatal(err)
	}

	r := &Service{
		p2p:         p,
		chain:       &mock.ChainService{State: s},
		initialSync: &mockSync.Sync{IsSyncing: false},
	}

	buf := new(bytes.Buffer)
	if _, err := p.Encoding().Encode(buf, slashing); err != nil {
		t.Fatal(err)
	}
	m := &pubsub.Message{
		Message: &pubsubpb.Message{
			Data: buf.Bytes(),
			TopicIDs: []string{
				p2p.GossipTypeMapping[reflect.TypeOf(slashing)],
			},
		},
	}

	valid := r.validateProposerSlashing(ctx, "", m)
	if !valid {
		t.Error("Failed validation")
	}
}

func TestValidateProposerSlashing_ValidSlashing_FromSelf(t *testing.T) {
	p := p2ptest.NewTestP2P(t)
	ctx := context.Background()

	slashing, s := setupValidProposerSlashing(t)

	r := &Service{
		p2p:         p,
		chain:       &mock.ChainService{State: s},
		initialSync: &mockSync.Sync{IsSyncing: false},
	}

	buf := new(bytes.Buffer)
	if _, err := p.Encoding().Encode(buf, slashing); err != nil {
		t.Fatal(err)
	}
	m := &pubsub.Message{
		Message: &pubsubpb.Message{
			Data: buf.Bytes(),
			TopicIDs: []string{
				p2p.GossipTypeMapping[reflect.TypeOf(slashing)],
			},
		},
	}
	valid := r.validateProposerSlashing(ctx, "", m)
	if valid {
		t.Error("Did not fail validation")
	}

	if p.BroadcastCalled {
		t.Error("Broadcast was called")
	}
}

func TestValidateProposerSlashing_ContextTimeout(t *testing.T) {
	p := p2ptest.NewTestP2P(t)

	slashing, state := setupValidProposerSlashing(t)
	slashing.Header_1.Slot = 100000000

	ctx, _ := context.WithTimeout(context.Background(), 100*time.Millisecond)

	r := &Service{
		p2p:         p,
		chain:       &mock.ChainService{State: state},
		initialSync: &mockSync.Sync{IsSyncing: false},
	}

	buf := new(bytes.Buffer)
	if _, err := p.Encoding().Encode(buf, slashing); err != nil {
		t.Fatal(err)
	}
	m := &pubsub.Message{
		Message: &pubsubpb.Message{
			Data: buf.Bytes(),
			TopicIDs: []string{
				p2p.GossipTypeMapping[reflect.TypeOf(slashing)],
			},
		},
	}
	valid := r.validateProposerSlashing(ctx, "", m)
	if valid {
		t.Error("slashing from the far distant future should have timed out and returned false")
	}
}

func TestValidateProposerSlashing_Syncing(t *testing.T) {
	p := p2ptest.NewTestP2P(t)
	ctx := context.Background()

	slashing, s := setupValidProposerSlashing(t)

	r := &Service{
		p2p:         p,
		chain:       &mock.ChainService{State: s},
		initialSync: &mockSync.Sync{IsSyncing: true},
	}

	buf := new(bytes.Buffer)
	if _, err := p.Encoding().Encode(buf, slashing); err != nil {
		t.Fatal(err)
	}
	m := &pubsub.Message{
		Message: &pubsubpb.Message{
			Data: buf.Bytes(),
			TopicIDs: []string{
				p2p.GossipTypeMapping[reflect.TypeOf(slashing)],
			},
		},
	}
	valid := r.validateProposerSlashing(ctx, "", m)

	if valid {
		t.Error("Did not fail validation")
	}
}
