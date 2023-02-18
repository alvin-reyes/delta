package jobs

import (
	"context"
	"delta/core"
	"delta/core/model"
	"delta/utils"
	"github.com/application-research/filclient"
	"github.com/ipfs/go-cid"
	"time"
)

type PieceCommpProcessor struct {
	Context         context.Context
	LightNode       *core.DeltaNode
	Content         model.Content
	DealPieceConfig filclient.DealConfig
}

func NewPieceCommpProcessor(ln *core.DeltaNode, content model.Content) IProcessor {
	return &PieceCommpProcessor{
		LightNode: ln,
		Content:   content,
		Context:   context.Background(),
	}
}

func (i PieceCommpProcessor) Run() error {

	i.LightNode.DB.Model(&model.Content{}).Where("id = ?", i.Content.ID).Updates(model.Content{Status: utils.CONTENT_PIECE_COMPUTING})
	payloadCid, err := cid.Decode(i.Content.Cid)
	if err != nil {
		i.LightNode.DB.Model(&model.Content{}).Where("id = ?", i.Content.ID).Updates(model.Content{Status: utils.CONTENT_PIECE_COMPUTING_FAILED, LastMessage: err.Error()})
	}

	// prepare the commp
	pieceCid, payloadSize, unpaddedPieceSize, err := filclient.GeneratePieceCommitment(i.Context, payloadCid, i.LightNode.Node.Blockstore)

	if err != nil {
		// put this back to the queue
		i.LightNode.Dispatcher.AddJobAndDispatch(NewPieceCommpProcessor(i.LightNode, i.Content), 1)
		return err
	}

	// save the commp to the database
	commpRec := &model.PieceCommitment{
		Cid:               payloadCid.String(),
		Piece:             pieceCid.String(),
		Size:              int64(payloadSize),
		PaddedPieceSize:   uint64(unpaddedPieceSize.Padded()),
		UnPaddedPieceSize: uint64(unpaddedPieceSize),
		Status:            "open",
		CreatedAt:         time.Now(),
		UpdatedAt:         time.Now(),
	}

	i.LightNode.DB.Create(commpRec)
	i.LightNode.DB.Model(&model.Content{}).Where("id = ?", i.Content.ID).Updates(model.Content{Status: utils.CONTENT_PIECE_ASSIGNED, PieceCommitmentId: commpRec.ID})

	// add this to the job queue
	item := NewStorageDealMakerProcessor(i.LightNode, i.Content, *commpRec)
	i.LightNode.Dispatcher.AddJobAndDispatch(item, 1)

	return nil
}
