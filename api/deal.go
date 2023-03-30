package api

import (
	"context"
	"database/sql"
	"delta/core"
	"delta/jobs"
	"delta/utils"
	"encoding/json"
	"fmt"
	model "github.com/application-research/delta-db/db_models"
	"github.com/ipfs/go-cid"
	ipld "github.com/ipfs/go-ipld-format"
	"github.com/pkg/errors"
	"go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/attribute"
	"gorm.io/gorm"
	"mime/multipart"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/labstack/echo/v4"
)

type CidRequest struct {
	Cids []string `json:"cids"`
}

type WalletRequest struct {
	Id         uint64 `json:"id,omitempty"`
	Address    string `json:"address,omitempty"`
	Uuid       string `json:"uuid,omitempty"`
	KeyType    string `json:"key_type,omitempty"`
	PrivateKey string `json:"private_key,omitempty"`
}

type PieceCommitmentRequest struct {
	Piece             string `json:"piece_cid,omitempty"`
	PaddedPieceSize   uint64 `json:"padded_piece_size,omitempty"`
	UnPaddedPieceSize uint64 `json:"unpadded_piece_size,omitempty"`
}

type DealRequest struct {
	Cid                  string                 `json:"cid,omitempty"`
	Miner                string                 `json:"miner,omitempty"`
	Duration             int64                  `json:"duration,omitempty"`
	DurationInDays       int64                  `json:"duration_in_days,omitempty"`
	Wallet               WalletRequest          `json:"wallet,omitempty"`
	PieceCommitment      PieceCommitmentRequest `json:"piece_commitment,omitempty"`
	ConnectionMode       string                 `json:"connection_mode,omitempty"`
	Size                 int64                  `json:"size,omitempty"`
	StartEpoch           int64                  `json:"start_epoch,omitempty"`
	StartEpochInDays     int64                  `json:"start_epoch_at_days,omitempty"`
	Replication          int64                  `json:"replication,omitempty"`
	RemoveUnsealedCopies bool                   `json:"remove_unsealed_copies,omitempty"`
	SkipIPNIAnnounce     bool                   `json:"skip_ipni_announce,omitempty"`
	Label                string                 `json:"label,omitempty"`
	DealVerifyState      string                 `json:"deal_verify_state,omitempty"`
}

// DealResponse Creating a new struct called DealResponse and then returning it.
type DealResponse struct {
	Status                       string      `json:"status"`
	Message                      string      `json:"message"`
	ContentId                    int64       `json:"content_id,omitempty"`
	DealRequest                  interface{} `json:"deal_request_meta,omitempty"`
	DealProposalParameterRequest interface{} `json:"deal_proposal_parameter_request_meta,omitempty"`
}

// ConfigureDealRouter It's a function that takes a pointer to an echo.Group and a pointer to a DeltaNode, and then it adds a bunch of routes
// to the echo.Group
// `ConfigureDealRouter` is a function that takes a `Group` and a `DeltaNode` and configures the `Group` to handle the
// `DeltaNode`'s deal-making functionality
func ConfigureDealRouter(e *echo.Group, node *core.DeltaNode) {

	statsService := core.NewStatsStatsService(node)
	dealMake := e.Group("/deal")

	// upload limiter middleware
	dealMake.Use(func(next echo.HandlerFunc) echo.HandlerFunc {
		return checkMetaFlags(next, node)
	})
	dealMake.Use(func(next echo.HandlerFunc) echo.HandlerFunc {
		return checkResourceLimits(next, node)
	})

	dealPrepare := dealMake.Group("/prepare")
	dealAnnounce := dealMake.Group("/announce")
	dealStatus := dealMake.Group("/status")

	dealMake.POST("/content", func(c echo.Context) error {
		return handleEndToEndDeal(c, node)
	})

	dealMake.POST("/end-to-end", func(c echo.Context) error {
		return handleEndToEndDeal(c, node)
	})

	dealMake.POST("/piece-commitment", func(c echo.Context) error {
		return handleImportDeal(c, node)
	})

	dealMake.POST("/import", func(c echo.Context) error {
		return handleImportDeal(c, node)
	})

	// make piece-commitments
	dealMake.POST("/piece-commitments", func(c echo.Context) error {
		return handleMultipleImportDeals(c, node)
	})

	dealMake.POST("/imports", func(c echo.Context) error {
		return handleMultipleImportDeals(c, node)
	})

	dealMake.POST("/existing/content", func(c echo.Context) error {
		return handleExistingContentAdd(c, node)
	})

	dealMake.POST("/existing/contents", func(c echo.Context) error {
		return handleExistingContentsAdd(c, node)
	})

	dealMake.POST("/existing/piece-commitment", func(c echo.Context) error {
		return handleImportDeal(c, node)
	})

	dealPrepare.POST("/content", func(c echo.Context) error {
		// TODO: call prepare unsigned deal proposal
		return nil
	})

	dealPrepare.POST("/piece-commitment", func(c echo.Context) error {
		// TODO: call prepare unsigned deal proposal with piece commitment
		return nil
	})

	dealPrepare.POST("/piece-commitments", func(c echo.Context) error {
		// TODO: call prepare unsigned deal proposal with piece commitments
		return nil
	})

	dealAnnounce.POST("/content", func(c echo.Context) error {
		// TODO: accept a hexed signed proposal
		return nil
	})

	dealAnnounce.POST("/piece-commitment", func(c echo.Context) error {
		return nil
	})

	dealAnnounce.POST("/piece-commitments", func(c echo.Context) error {
		return nil
	})

	dealStatus.POST("/content/:contentId", func(c echo.Context) error {
		return handleContentStats(c, *statsService)
	})
	dealStatus.POST("/piece-commitment/:piece-commitmentId", func(c echo.Context) error {
		return handleCommitmentPieceStats(c, *statsService)

	})
}

// > check if the sum(size) transfer-started and created_at within instance_start time
//
// The above function is a middleware that checks if the sum of the size of all the files that have been transferred and
// created_at within instance_start time
func checkMetaFlags(next echo.HandlerFunc, node *core.DeltaNode) func(c echo.Context) error {
	return func(c echo.Context) error {

		_, span := otel.Tracer("handleNodePeers").Start(context.Background(), "handleNodeHostApiKey")
		defer span.End()

		span.SetName("checkMetaFlags")
		span.SetAttributes(attribute.String("user-agent", c.Request().UserAgent()))
		span.SetAttributes(attribute.String("path", c.Path()))
		span.SetAttributes(attribute.String("method", c.Request().Method))
		span.SetAttributes(attribute.String("remote_ip", c.RealIP()))
		span.SetAttributes(attribute.String("host", c.Request().Host))
		span.SetAttributes(attribute.String("referer", c.Request().Referer()))
		span.SetAttributes(attribute.String("request_uri", c.Request().RequestURI))

		// check if the sum(size) transfer-started and created_at within instance_start time
		var meta model.InstanceMeta
		node.DB.First(&meta)

		if meta.DisableRequest {
			return c.JSON(http.StatusForbidden, "request is disabled")
		}
		return next(c)
	}
}

// It checks if the sum of the size of all the files that are currently being transferred is greater than the number of
// CPUs multiplied by the number of bytes per CPU. If it is, then it returns an error
func checkResourceLimits(next echo.HandlerFunc, node *core.DeltaNode) func(c echo.Context) error {
	return func(c echo.Context) error {

		_, span := otel.Tracer("handleNodePeers").Start(context.Background(), "handleNodeHostApiKey")
		defer span.End()

		span.SetName("checkMetaFlags")
		span.SetAttributes(attribute.String("user-agent", c.Request().UserAgent()))
		span.SetAttributes(attribute.String("path", c.Path()))
		span.SetAttributes(attribute.String("method", c.Request().Method))
		span.SetAttributes(attribute.String("remote_ip", c.RealIP()))
		span.SetAttributes(attribute.String("host", c.Request().Host))
		span.SetAttributes(attribute.String("referer", c.Request().Referer()))
		span.SetAttributes(attribute.String("request_uri", c.Request().RequestURI))

		var size sql.NullInt64
		node.DB.Raw("select sum(size) from contents where status = 'transfer-started' and created_at > ?", node.MetaInfo.InstanceStart).Scan(&size)

		// memory limit (10GB per CPU)
		if size != (sql.NullInt64{}) {
			if uint64(size.Int64) > (node.MetaInfo.NumberOfCpus * node.MetaInfo.BytesPerCpu) {
				return c.JSON(http.StatusForbidden, DealResponse{
					Status:  "error",
					Message: "Too much data is being transferred, please try again once all other transfers are complete",
				})
			}
		}
		return next(c)
	}
}

// handleExistingContentsAdd handles the request to add existing content to the network
// @Summary Add existing content to the network
// @Description Add existing content to the network
// @Tags deal
// @Accept  json
// @Produce  json
func handleExistingContentsAdd(c echo.Context, node *core.DeltaNode) error {
	var dealRequests []DealRequest

	// lets record this.
	authorizationString := c.Request().Header.Get("Authorization")
	authParts := strings.Split(authorizationString, " ")

	//	validate the meta
	err := c.Bind(&dealRequests)

	if err != nil {
		return errors.New("Error parsing the request, please check the request body if it complies with the spec")
	}

	errTxn := node.DB.Transaction(func(tx *gorm.DB) error {
		var dealResponses []DealResponse
		for _, dealRequest := range dealRequests {
			err = ValidateMeta(dealRequest)
			if err != nil {
				// return the error from the validation
				return err
			}

			decodeCid, err := cid.Decode(dealRequest.Cid)
			if err != nil {
				return errors.New("Error decoding the cid")
			}

			addNode, err := node.Node.Get(context.Background(), decodeCid)
			if err != nil {
				return errors.New("Error pinning the file")
			}

			// specify the connection mode
			var connMode = dealRequest.ConnectionMode
			if connMode == "" || (connMode != utils.CONNECTION_MODE_E2E && connMode != utils.CONNECTION_MODE_IMPORT) {
				connMode = "e2e"
			}

			// let's create a commp but only if we have
			// a cid, a piece_cid, a padded_piece_size, size
			var pieceCommp model.PieceCommitment
			if (PieceCommitmentRequest{} != dealRequest.PieceCommitment && dealRequest.PieceCommitment.Piece != "") &&
				(dealRequest.PieceCommitment.PaddedPieceSize != 0) &&
				(dealRequest.Size != 0) {

				// if commp is there, make sure the piece and size are there. Use default duration.
				pieceCommp.Cid = addNode.Cid().String()
				pieceCommp.Piece = dealRequest.PieceCommitment.Piece
				pieceCommp.Size = dealRequest.Size
				pieceCommp.UnPaddedPieceSize = dealRequest.PieceCommitment.UnPaddedPieceSize
				pieceCommp.PaddedPieceSize = dealRequest.PieceCommitment.PaddedPieceSize
				pieceCommp.CreatedAt = time.Now()
				pieceCommp.UpdatedAt = time.Now()
				pieceCommp.Status = utils.COMMP_STATUS_OPEN
				tx.Create(&pieceCommp)
				dealRequest.PieceCommitment = PieceCommitmentRequest{
					Piece:             pieceCommp.Piece,
					PaddedPieceSize:   pieceCommp.PaddedPieceSize,
					UnPaddedPieceSize: pieceCommp.UnPaddedPieceSize,
				}
			}

			// save the content to the DB with the piece_commitment_id
			cidName := addNode.Cid().String()
			cidSize, err := addNode.Size()
			if err != nil {
				return errors.New("Error getting the size of the cid")
			}
			content := model.Content{
				Name:              cidName,
				Size:              int64(cidSize),
				Cid:               addNode.Cid().String(),
				RequestingApiKey:  authParts[1],
				PieceCommitmentId: pieceCommp.ID,
				Status:            utils.CONTENT_PINNED,
				ConnectionMode:    connMode,
				CreatedAt:         time.Now(),
				UpdatedAt:         time.Now(),
			}
			tx.Create(&content)
			dealRequest.Cid = content.Cid

			//	assign a miner
			if dealRequest.Miner != "" {
				contentMinerAssignment := model.ContentMiner{
					Miner:     dealRequest.Miner,
					Content:   content.ID,
					CreatedAt: time.Now(),
					UpdatedAt: time.Now(),
				}
				node.DB.Create(&contentMinerAssignment)
				dealRequest.Miner = contentMinerAssignment.Miner
			}

			if (WalletRequest{} != dealRequest.Wallet) {

				// get wallet from wallets database
				var wallet model.Wallet

				if dealRequest.Wallet.Address != "" {
					tx.Where("addr = ? and owner = ?", dealRequest.Wallet.Address, authParts[1]).First(&wallet)
				} else if dealRequest.Wallet.Uuid != "" {
					tx.Where("uuid = ? and owner = ?", dealRequest.Wallet.Uuid, authParts[1]).First(&wallet)
				} else {
					tx.Where("id = ? and owner = ?", dealRequest.Wallet.Id, authParts[1]).First(&wallet)
				}

				if wallet.ID == 0 {
					return errors.New("Wallet not found, please make sure the wallet is registered")
				}

				// create the wallet request object
				var hexedWallet WalletRequest
				hexedWallet.KeyType = wallet.KeyType
				hexedWallet.PrivateKey = wallet.PrivateKey

				if err != nil {
					return errors.New("Error encoding the wallet")
				}

				// assign the wallet to the content
				contentWalletAssignment := model.ContentWallet{
					WalletId:  wallet.ID,
					Content:   content.ID,
					CreatedAt: time.Now(),
					UpdatedAt: time.Now(),
				}
				node.DB.Create(&contentWalletAssignment)

				dealRequest.Wallet = WalletRequest{
					Id:      dealRequest.Wallet.Id,
					Address: wallet.Addr,
				}
			}

			var dealProposalParam model.ContentDealProposalParameters
			dealProposalParam.CreatedAt = time.Now()
			dealProposalParam.UpdatedAt = time.Now()
			dealProposalParam.Content = content.ID

			dealProposalParam.Label = func() string {
				if dealRequest.Label != "" {
					return dealRequest.Label
				}
				return content.Cid
			}()
			dealProposalParam.SkipIPNIAnnounce = dealRequest.SkipIPNIAnnounce
			dealProposalParam.VerifiedDeal = func() bool {
				if dealRequest.DealVerifyState == utils.DEAL_VERIFIED {
					return true
				}
				return false
			}()

			// start epoch
			if dealRequest.StartEpoch != 0 {
				dealProposalParam.StartEpoch = dealRequest.StartEpoch
			}
			// duration
			if dealRequest.Duration == 0 {
				dealProposalParam.Duration = utils.DEFAULT_DURATION
			} else {
				dealProposalParam.Duration = dealRequest.Duration
			}

			if dealRequest.StartEpochInDays != 0 {
				startEpochTime := time.Now().AddDate(0, 0, int(dealRequest.StartEpochInDays))
				dealRequest.StartEpoch = utils.DateToHeight(startEpochTime)
				dealRequest.StartEpoch = dealRequest.StartEpoch + (utils.EPOCH_PER_HOUR * 24 * 7)
			}

			if dealRequest.DurationInDays > 540 {
				return errors.New("Duration cannot be more than 540 days")
			}

			if dealRequest.DurationInDays != 0 {
				dealProposalParam.Duration = utils.EPOCH_PER_DAY * (dealRequest.DurationInDays - 7)
			}
			// remove unsealed copy
			if dealRequest.RemoveUnsealedCopies == false {
				dealProposalParam.RemoveUnsealedCopy = false
			} else {
				dealProposalParam.RemoveUnsealedCopy = true
			}

			// deal proposal parameters
			tx.Create(&dealProposalParam)

			if err != nil {
				return errors.New("Error pinning the file")
			}

			var dispatchJobs core.IProcessor
			if pieceCommp.ID != 0 {
				dispatchJobs = jobs.NewStorageDealMakerProcessor(node, content, pieceCommp) // straight to storage deal making
			} else {
				dispatchJobs = jobs.NewPieceCommpProcessor(node, content) // straight to pieceCommp
			}

			node.Dispatcher.AddJob(dispatchJobs)

			dealResponses = append(dealResponses, DealResponse{
				Status:                       "success",
				Message:                      "Deal request received. Please take note and check the status of the deal using the content_id.",
				ContentId:                    content.ID,
				DealRequest:                  dealRequest,
				DealProposalParameterRequest: dealProposalParam,
			})
		}
		node.Dispatcher.Start(len(dealRequests))
		return c.JSON(http.StatusOK, dealResponses)
	})
	if errTxn != nil {
		return errors.New("Error creating the transaction")
	}
	return nil
}

// handleExistingContentAdd handles the request to add content to the network
// @Summary Add content to the network
// @Description Add content to the network
// @Tags Content
// @Accept  json
// @Produce  json
func handleExistingContentAdd(c echo.Context, node *core.DeltaNode) error {
	var dealRequest DealRequest

	// lets record this.
	authorizationString := c.Request().Header.Get("Authorization")
	authParts := strings.Split(authorizationString, " ")
	err := c.Bind(&dealRequest)
	err = ValidateMeta(dealRequest)
	if err != nil {
		// return the error from the validation
		return err
	}

	decodeCid, err := cid.Decode(dealRequest.Cid)
	if err != nil {
		return errors.New("Error decoding the cid")
	}

	addNode, err := node.Node.Get(context.Background(), decodeCid)
	if err != nil {
		return errors.New("Error pinning the file")
	}

	// specify the connection mode
	var connMode = dealRequest.ConnectionMode
	if connMode == "" || (connMode != utils.CONNECTION_MODE_E2E && connMode != utils.CONNECTION_MODE_IMPORT) {
		connMode = "e2e"
	}

	errTxn := node.DB.Transaction(func(tx *gorm.DB) error {
		// let's create a commp but only if we have
		// a cid, a piece_cid, a padded_piece_size, size
		var pieceCommp model.PieceCommitment
		if (PieceCommitmentRequest{} != dealRequest.PieceCommitment && dealRequest.PieceCommitment.Piece != "") &&
			(dealRequest.PieceCommitment.PaddedPieceSize != 0) &&
			(dealRequest.Size != 0) {

			// if commp is there, make sure the piece and size are there. Use default duration.
			pieceCommp.Cid = addNode.Cid().String()
			pieceCommp.Piece = dealRequest.PieceCommitment.Piece
			pieceCommp.Size = dealRequest.Size
			pieceCommp.UnPaddedPieceSize = dealRequest.PieceCommitment.UnPaddedPieceSize
			pieceCommp.PaddedPieceSize = dealRequest.PieceCommitment.PaddedPieceSize
			pieceCommp.CreatedAt = time.Now()
			pieceCommp.UpdatedAt = time.Now()
			pieceCommp.Status = utils.COMMP_STATUS_OPEN
			node.DB.Create(&pieceCommp)

			dealRequest.PieceCommitment = PieceCommitmentRequest{
				Piece:             pieceCommp.Piece,
				PaddedPieceSize:   pieceCommp.PaddedPieceSize,
				UnPaddedPieceSize: pieceCommp.UnPaddedPieceSize,
			}
		}

		// save the content to the DB with the piece_commitment_id
		cidName := addNode.Cid().String()
		cidSize, err := addNode.Size()
		if err != nil {
			return errors.New("Error getting the size of the cid")
		}
		content := model.Content{
			Name:              cidName,
			Size:              int64(cidSize),
			Cid:               addNode.Cid().String(),
			RequestingApiKey:  authParts[1],
			PieceCommitmentId: pieceCommp.ID,
			Status:            utils.CONTENT_PINNED,
			ConnectionMode:    connMode,
			CreatedAt:         time.Now(),
			UpdatedAt:         time.Now(),
		}
		node.DB.Create(&content)
		dealRequest.Cid = content.Cid

		//	assign a miner
		if dealRequest.Miner != "" {
			contentMinerAssignment := model.ContentMiner{
				Miner:     dealRequest.Miner,
				Content:   content.ID,
				CreatedAt: time.Now(),
				UpdatedAt: time.Now(),
			}
			node.DB.Create(&contentMinerAssignment)
			dealRequest.Miner = contentMinerAssignment.Miner
		}

		// 	assign a wallet_estuary
		if (WalletRequest{} != dealRequest.Wallet) {

			// get wallet from wallets database
			var wallet model.Wallet

			if dealRequest.Wallet.Address != "" {
				node.DB.Where("addr = ? and owner = ?", dealRequest.Wallet.Address, authParts[1]).First(&wallet)
			} else if dealRequest.Wallet.Uuid != "" {
				node.DB.Where("uuid = ? and owner = ?", dealRequest.Wallet.Uuid, authParts[1]).First(&wallet)
			} else {
				node.DB.Where("id = ? and owner = ?", dealRequest.Wallet.Id, authParts[1]).First(&wallet)
			}

			if wallet.ID == 0 {
				return errors.New("Wallet not found, please make sure the wallet is registered")
			}

			// create the wallet request object
			var hexedWallet WalletRequest
			hexedWallet.KeyType = wallet.KeyType
			hexedWallet.PrivateKey = wallet.PrivateKey

			if err != nil {
				return errors.New("Error encoding the wallet")
			}

			// assign the wallet to the content
			contentWalletAssignment := model.ContentWallet{
				WalletId:  wallet.ID,
				Content:   content.ID,
				CreatedAt: time.Now(),
				UpdatedAt: time.Now(),
			}
			node.DB.Create(&contentWalletAssignment)

			dealRequest.Wallet = WalletRequest{
				Id:      dealRequest.Wallet.Id,
				Address: wallet.Addr,
			}
		}

		var dealProposalParam model.ContentDealProposalParameters
		dealProposalParam.CreatedAt = time.Now()
		dealProposalParam.UpdatedAt = time.Now()
		dealProposalParam.Content = content.ID
		dealProposalParam.Label = func() string {
			if dealRequest.Label != "" {
				return dealRequest.Label
			}
			return content.Cid
		}()
		dealProposalParam.SkipIPNIAnnounce = dealRequest.SkipIPNIAnnounce
		dealProposalParam.VerifiedDeal = func() bool {
			if dealRequest.DealVerifyState == utils.DEAL_VERIFIED {
				return true
			}
			return false
		}()

		// start epoch
		if dealRequest.StartEpoch != 0 {
			dealProposalParam.StartEpoch = dealRequest.StartEpoch
		}
		// duration
		if dealRequest.Duration == 0 {
			dealProposalParam.Duration = utils.DEFAULT_DURATION
		} else {
			dealProposalParam.Duration = dealRequest.Duration
		}

		if dealRequest.StartEpochInDays != 0 {
			startEpochTime := time.Now().AddDate(0, 0, int(dealRequest.StartEpochInDays))
			dealRequest.StartEpoch = utils.DateToHeight(startEpochTime)
			dealRequest.StartEpoch = dealRequest.StartEpoch + (utils.EPOCH_PER_HOUR * 24 * 7)
		}

		if dealRequest.DurationInDays > 540 {
			return errors.New("Duration cannot be more than 540 days")
		}

		if dealRequest.DurationInDays != 0 {
			dealProposalParam.Duration = utils.EPOCH_PER_DAY * (dealRequest.DurationInDays - 7)
		}

		// remove unsealed copy
		if dealRequest.RemoveUnsealedCopies == false {
			dealProposalParam.RemoveUnsealedCopy = false
		} else {
			dealProposalParam.RemoveUnsealedCopy = true
		}

		// deal proposal parameters
		node.DB.Create(&dealProposalParam)

		if err != nil {
			return errors.New("Error pinning the file")
		}

		var dispatchJobs core.IProcessor
		if pieceCommp.ID != 0 {
			dispatchJobs = jobs.NewStorageDealMakerProcessor(node, content, pieceCommp) // straight to storage deal making
		} else {
			dispatchJobs = jobs.NewPieceCommpProcessor(node, content) // straight to pieceCommp
		}

		node.Dispatcher.AddJobAndDispatch(dispatchJobs, 1)

		err = c.JSON(200, DealResponse{
			Status:                       "success",
			Message:                      "Deal request received. Please take note of the content_id. You can use the content_id to check the status of the deal.",
			ContentId:                    content.ID,
			DealRequest:                  dealRequest,
			DealProposalParameterRequest: dealProposalParam,
		})
		if err != nil {
			return err
		}
		return nil
	})

	if errTxn != nil {
		return errors.New("Error creating the content record" + " " + errTxn.Error())
	}
	return nil
}

func handleEndToEndDeal(c echo.Context, node *core.DeltaNode) error {
	var dealRequest DealRequest
	// lets record this.
	authorizationString := c.Request().Header.Get("Authorization")
	authParts := strings.Split(authorizationString, " ")
	file, err := c.FormFile("data") // file
	meta := c.FormValue("metadata")

	//	validate the meta
	err = json.Unmarshal([]byte(meta), &dealRequest)
	if err != nil {
		return err
	}

	if dealRequest.ConnectionMode == "import" {
		return errors.New("Connection mode import is not supported for end-to-end deal endpoint")
	}

	// fail safe
	dealRequest.ConnectionMode = "e2e"

	err = ValidateMeta(dealRequest)

	if err != nil {
		// return the error from the validation
		return err
	}

	// process the file
	src, err := file.Open()
	if err != nil {
		return errors.New("Error opening the file")
	}

	addNode, err := node.Node.AddPinFile(c.Request().Context(), src, nil)
	if err != nil {
		return errors.New("Error pinning the file")
	}

	// wrap in a transaction so we can rollback if something goes wrong
	errTxn := node.DB.Transaction(func(tx *gorm.DB) error {

		// let's create a commp but only if we have
		// a cid, a piece_cid, a padded_piece_size, size
		var pieceCommp model.PieceCommitment
		if (PieceCommitmentRequest{} != dealRequest.PieceCommitment && dealRequest.PieceCommitment.Piece != "") &&
			(dealRequest.PieceCommitment.PaddedPieceSize != 0) &&
			(dealRequest.Size != 0) {

			// if commp is there, make sure the piece and size are there. Use default duration.
			pieceCommp.Cid = addNode.Cid().String()
			pieceCommp.Piece = dealRequest.PieceCommitment.Piece
			pieceCommp.Size = file.Size
			pieceCommp.UnPaddedPieceSize = dealRequest.PieceCommitment.UnPaddedPieceSize
			pieceCommp.PaddedPieceSize = dealRequest.PieceCommitment.PaddedPieceSize
			pieceCommp.CreatedAt = time.Now()
			pieceCommp.UpdatedAt = time.Now()
			pieceCommp.Status = utils.COMMP_STATUS_OPEN
			tx.Create(&pieceCommp)

			dealRequest.PieceCommitment = PieceCommitmentRequest{
				Piece:             pieceCommp.Piece,
				PaddedPieceSize:   pieceCommp.PaddedPieceSize,
				UnPaddedPieceSize: pieceCommp.UnPaddedPieceSize,
			}
		}

		// save the content to the DB with the piece_commitment_id
		content := model.Content{
			Name:              file.Filename,
			Size:              file.Size,
			Cid:               addNode.Cid().String(),
			RequestingApiKey:  authParts[1],
			PieceCommitmentId: pieceCommp.ID,
			Status:            utils.CONTENT_PINNED,
			ConnectionMode:    dealRequest.ConnectionMode,
			CreatedAt:         time.Now(),
			UpdatedAt:         time.Now(),
		}
		tx.Create(&content)
		dealRequest.Cid = content.Cid

		//	assign a miner
		if dealRequest.Miner != "" {
			contentMinerAssignment := model.ContentMiner{
				Miner:     dealRequest.Miner,
				Content:   content.ID,
				CreatedAt: time.Now(),
				UpdatedAt: time.Now(),
			}
			tx.Create(&contentMinerAssignment)
			dealRequest.Miner = contentMinerAssignment.Miner
		}

		if (WalletRequest{} != dealRequest.Wallet) {

			// get wallet from wallets database
			var wallet model.Wallet

			if dealRequest.Wallet.Address != "" {
				tx.Where("addr = ? and owner = ?", dealRequest.Wallet.Address, authParts[1]).First(&wallet)
			} else if dealRequest.Wallet.Uuid != "" {
				tx.Where("uuid = ? and owner = ?", dealRequest.Wallet.Uuid, authParts[1]).First(&wallet)
			} else {
				tx.Where("id = ? and owner = ?", dealRequest.Wallet.Id, authParts[1]).First(&wallet)
			}

			if wallet.ID == 0 {
				return errors.New("Wallet not found, please make sure the wallet is registered")
			}

			// create the wallet request object
			var hexedWallet WalletRequest
			hexedWallet.KeyType = wallet.KeyType
			hexedWallet.PrivateKey = wallet.PrivateKey

			if err != nil {
				return errors.New("Error encoding the wallet")
			}

			// assign the wallet to the content
			contentWalletAssignment := model.ContentWallet{
				WalletId:  wallet.ID,
				Content:   content.ID,
				CreatedAt: time.Now(),
				UpdatedAt: time.Now(),
			}
			tx.Create(&contentWalletAssignment)

			dealRequest.Wallet = WalletRequest{
				Id:      dealRequest.Wallet.Id,
				Address: wallet.Addr,
			}
		}

		var dealProposalParam model.ContentDealProposalParameters
		dealProposalParam.CreatedAt = time.Now()
		dealProposalParam.UpdatedAt = time.Now()
		dealProposalParam.Content = content.ID
		dealProposalParam.Label = func() string {
			if dealRequest.Label != "" {
				return dealRequest.Label
			}
			return content.Cid
		}()
		dealProposalParam.SkipIPNIAnnounce = dealRequest.SkipIPNIAnnounce
		dealProposalParam.VerifiedDeal = func() bool {
			if dealRequest.DealVerifyState == utils.DEAL_VERIFIED {
				return true
			}
			return false
		}()
		// start epoch
		if dealRequest.StartEpoch != 0 {
			dealProposalParam.StartEpoch = dealRequest.StartEpoch
		}
		// duration
		if dealRequest.Duration == 0 {
			dealProposalParam.Duration = utils.DEFAULT_DURATION
		} else {
			dealProposalParam.Duration = dealRequest.Duration
		}

		if dealRequest.StartEpochInDays != 0 {
			startEpochTime := time.Now().AddDate(0, 0, int(dealRequest.StartEpochInDays))
			dealRequest.StartEpoch = utils.DateToHeight(startEpochTime)
			dealRequest.StartEpoch = dealRequest.StartEpoch + (utils.EPOCH_PER_HOUR * 24 * 7)
			dealProposalParam.StartEpoch = dealRequest.StartEpoch
		}

		if dealRequest.DurationInDays > 540 {
			return errors.New("Duration cannot be more than 540 days")
		}

		if dealRequest.DurationInDays != 0 {
			dealProposalParam.Duration = utils.EPOCH_PER_DAY * (dealRequest.DurationInDays - 7)
		}

		// remove unsealed copy
		if dealRequest.RemoveUnsealedCopies == false {
			dealProposalParam.RemoveUnsealedCopy = false
		} else {
			dealProposalParam.RemoveUnsealedCopy = true
		}

		// deal proposal parameters
		tx.Create(&dealProposalParam)

		var dispatchJobs core.IProcessor
		if pieceCommp.ID != 0 {
			dispatchJobs = jobs.NewStorageDealMakerProcessor(node, content, pieceCommp) // straight to storage deal making
		} else {
			dispatchJobs = jobs.NewPieceCommpProcessor(node, content) // straight to pieceCommp
		}

		node.Dispatcher.AddJobAndDispatch(dispatchJobs, 1)

		err = c.JSON(200, DealResponse{
			Status:                       "success",
			Message:                      "Deal request received. Please take note of the content_id. You can use the content_id to check the status of the deal.",
			ContentId:                    content.ID,
			DealRequest:                  dealRequest,
			DealProposalParameterRequest: dealProposalParam,
		})
		if err != nil {
			return err
		}

		// return transaction
		return nil
	})

	if errTxn != nil {
		return errors.New("Error creating the content record" + " " + errTxn.Error())
	}

	return nil
}

// handleImportDeal handles the request to add a commp record.
// @Summary Add a commp record
// @Description Add a commp record
// @Tags deals
// @Accept  json
// @Produce  json
func handleImportDeal(c echo.Context, node *core.DeltaNode) error {
	var dealRequest DealRequest

	// lets record this.
	authorizationString := c.Request().Header.Get("Authorization")
	authParts := strings.Split(authorizationString, " ")
	err := c.Bind(&dealRequest)

	if err != nil {
		return errors.New("Error parsing the request, please check the request body if it complies with the spec")
	}

	if dealRequest.ConnectionMode == "e2e" {
		return errors.New("Connection mode e2e is not supported on this import endpoint")
	}

	dealRequest.ConnectionMode = "import"
	err = ValidateMeta(dealRequest)

	if err != nil {
		return err
	}

	// specify the connection mode
	var connMode = dealRequest.ConnectionMode
	if connMode == "" || (connMode != utils.CONNECTION_MODE_E2E && connMode != utils.CONNECTION_MODE_IMPORT) {
		connMode = "e2e"
	}

	err = ValidatePieceCommitmentMeta(dealRequest.PieceCommitment)
	if err != nil {
		return err
	}

	errTxn := node.DB.Transaction(func(tx *gorm.DB) error {
		// let's create a commp but only if we have
		// a cid, a piece_cid, a padded_piece_size, size
		var pieceCommp model.PieceCommitment
		if (PieceCommitmentRequest{} != dealRequest.PieceCommitment && dealRequest.PieceCommitment.Piece != "") &&
			(dealRequest.PieceCommitment.PaddedPieceSize != 0) &&
			(dealRequest.Size != 0) {

			// if commp is there, make sure the piece and size are there. Use default duration.
			pieceCommp.Cid = dealRequest.Cid
			pieceCommp.Piece = dealRequest.PieceCommitment.Piece
			pieceCommp.Size = dealRequest.Size
			pieceCommp.UnPaddedPieceSize = dealRequest.PieceCommitment.UnPaddedPieceSize
			pieceCommp.PaddedPieceSize = dealRequest.PieceCommitment.PaddedPieceSize
			pieceCommp.CreatedAt = time.Now()
			pieceCommp.UpdatedAt = time.Now()
			pieceCommp.Status = utils.COMMP_STATUS_COMITTED
			tx.Create(&pieceCommp)

			dealRequest.PieceCommitment = PieceCommitmentRequest{
				Piece:             pieceCommp.Piece,
				PaddedPieceSize:   pieceCommp.PaddedPieceSize,
				UnPaddedPieceSize: pieceCommp.UnPaddedPieceSize,
			}
		}

		// save the content to the DB with the piece_commitment_id
		content := model.Content{
			Name:              dealRequest.Cid,
			Size:              dealRequest.Size,
			Cid:               dealRequest.Cid,
			RequestingApiKey:  authParts[1],
			PieceCommitmentId: pieceCommp.ID,
			Status:            utils.CONTENT_DEAL_MAKING_PROPOSAL,
			ConnectionMode:    connMode,
			CreatedAt:         time.Now(),
			UpdatedAt:         time.Now(),
		}
		tx.Create(&content)
		dealRequest.Cid = content.Cid

		//	assign a miner
		if dealRequest.Miner != "" {
			contentMinerAssignment := model.ContentMiner{
				Miner:     dealRequest.Miner,
				Content:   content.ID,
				CreatedAt: time.Now(),
				UpdatedAt: time.Now(),
			}
			tx.Create(&contentMinerAssignment)
			dealRequest.Miner = contentMinerAssignment.Miner
		}

		if (WalletRequest{} != dealRequest.Wallet) {

			// get wallet from wallets database
			var wallet model.Wallet
			if dealRequest.Wallet.Address != "" {
				tx.Where("addr = ? and owner = ?", dealRequest.Wallet.Address, authParts[1]).First(&wallet)
			} else if dealRequest.Wallet.Uuid != "" {
				tx.Where("uuid = ? and owner = ?", dealRequest.Wallet.Uuid, authParts[1]).First(&wallet)
			} else {
				tx.Where("id = ? and owner = ?", dealRequest.Wallet.Id, authParts[1]).First(&wallet)
			}

			if wallet.ID == 0 {
				return errors.New("Wallet not found, please make sure the wallet is registered")
			}

			// create the wallet request object
			var hexedWallet WalletRequest
			hexedWallet.KeyType = wallet.KeyType
			hexedWallet.PrivateKey = wallet.PrivateKey

			if err != nil {
				return errors.New("Error encoding the wallet")
			}

			// assign the wallet to the content
			contentWalletAssignment := model.ContentWallet{
				WalletId:  wallet.ID,
				Content:   content.ID,
				CreatedAt: time.Now(),
				UpdatedAt: time.Now(),
			}
			tx.Create(&contentWalletAssignment)

			dealRequest.Wallet = WalletRequest{
				Id:      dealRequest.Wallet.Id,
				Address: wallet.Addr,
			}
		}

		var dealProposalParam model.ContentDealProposalParameters
		dealProposalParam.CreatedAt = time.Now()
		dealProposalParam.UpdatedAt = time.Now()
		dealProposalParam.Content = content.ID
		dealProposalParam.Label = func() string {
			if dealRequest.Label != "" {
				return dealRequest.Label
			}
			return content.Cid
		}()

		dealProposalParam.SkipIPNIAnnounce = dealRequest.SkipIPNIAnnounce
		dealProposalParam.VerifiedDeal = func() bool {
			if dealRequest.DealVerifyState == utils.DEAL_VERIFIED {
				return true
			}
			return false
		}()
		// start epoch
		if dealRequest.StartEpoch != 0 {
			dealProposalParam.StartEpoch = dealRequest.StartEpoch
		}
		// duration
		if dealRequest.Duration == 0 {
			dealProposalParam.Duration = utils.DEFAULT_DURATION
		} else {
			dealProposalParam.Duration = dealRequest.Duration
		}

		if dealRequest.StartEpochInDays != 0 {
			startEpochTime := time.Now().AddDate(0, 0, int(dealRequest.StartEpochInDays))
			dealRequest.StartEpoch = utils.DateToHeight(startEpochTime)
			dealRequest.StartEpoch = dealRequest.StartEpoch + (utils.EPOCH_PER_HOUR * 24 * 7)
			dealProposalParam.StartEpoch = dealRequest.StartEpoch
		}

		if dealRequest.DurationInDays > 540 {
			return errors.New("Duration cannot be more than 540 days")
		}

		if dealRequest.DurationInDays != 0 {
			dealProposalParam.Duration = utils.EPOCH_PER_DAY * (dealRequest.DurationInDays - 7)
		}

		// remove unsealed copy
		if dealRequest.RemoveUnsealedCopies == false {
			dealProposalParam.RemoveUnsealedCopy = false
		} else {
			dealProposalParam.RemoveUnsealedCopy = true
		}

		// deal proposal parameters
		tx.Create(&dealProposalParam)

		if err != nil {
			return errors.New("Error parsing the request, please check the request body if it complies with the spec")
		}

		var dispatchJobs core.IProcessor
		if pieceCommp.ID != 0 {
			dispatchJobs = jobs.NewStorageDealMakerProcessor(node, content, pieceCommp) // straight to storage deal making
		}

		node.Dispatcher.AddJobAndDispatch(dispatchJobs, 1)

		err = c.JSON(200, DealResponse{
			Status:                       "success",
			Message:                      "Deal request received. Please take note of the content_id. You can use the content_id to check the status of the deal.",
			ContentId:                    content.ID,
			DealRequest:                  dealRequest,
			DealProposalParameterRequest: dealProposalParam,
		})
		if err != nil {
			return err
		}
		//
		return nil
	})

	if errTxn != nil {
		return errors.New("Error creating the piece-commitment record" + " " + errTxn.Error())
	}
	return nil
}

// handleMultipleImportDeals handles the request to add a commp record.
// @Summary Add a commp record
// @Description Add a commp record
// @Tags CommP
// @Accept  json
// @Produce  json
func handleMultipleImportDeals(c echo.Context, node *core.DeltaNode) error {
	var dealRequests []DealRequest

	// lets record this.
	authorizationString := c.Request().Header.Get("Authorization")
	authParts := strings.Split(authorizationString, " ")

	//	validate the meta
	err := c.Bind(&dealRequests)
	if err != nil {
		return errors.New("Error parsing the request, please check the request body if it complies with the spec")
	}

	errTxn := node.DB.Transaction(func(tx *gorm.DB) error {
		var dealResponses []DealResponse
		for _, dealRequest := range dealRequests {
			if dealRequest.ConnectionMode == "e2e" {
				return errors.New("Connection mode e2e is not supported on this import endpoint")
			}
			dealRequest.ConnectionMode = "import"
			err = ValidateMeta(dealRequest)
			if err != nil {
				tx.Rollback()
				return err
			}

			// specify the connection mode
			var connMode = dealRequest.ConnectionMode
			if connMode == "" || (connMode != utils.CONNECTION_MODE_E2E && connMode != utils.CONNECTION_MODE_IMPORT) {
				connMode = "e2e"
			}

			err = ValidatePieceCommitmentMeta(dealRequest.PieceCommitment)
			if err != nil {
				tx.Rollback()
				return err
			}

			// let's create a commp but only if we have
			// a cid, a piece_cid, a padded_piece_size, size
			var pieceCommp model.PieceCommitment
			if (PieceCommitmentRequest{} != dealRequest.PieceCommitment && dealRequest.PieceCommitment.Piece != "") &&
				(dealRequest.PieceCommitment.PaddedPieceSize != 0) &&
				(dealRequest.Size != 0) {

				// if commp is there, make sure the piece and size are there. Use default duration.
				pieceCommp.Cid = dealRequest.Cid
				pieceCommp.Piece = dealRequest.PieceCommitment.Piece
				pieceCommp.Size = dealRequest.Size
				pieceCommp.UnPaddedPieceSize = dealRequest.PieceCommitment.UnPaddedPieceSize
				pieceCommp.PaddedPieceSize = dealRequest.PieceCommitment.PaddedPieceSize
				pieceCommp.CreatedAt = time.Now()
				pieceCommp.UpdatedAt = time.Now()
				pieceCommp.Status = utils.COMMP_STATUS_COMITTED
				tx.Create(&pieceCommp)

				dealRequest.PieceCommitment = PieceCommitmentRequest{
					Piece:             pieceCommp.Piece,
					PaddedPieceSize:   pieceCommp.PaddedPieceSize,
					UnPaddedPieceSize: pieceCommp.UnPaddedPieceSize,
				}
			}

			// save the content to the DB with the piece_commitment_id
			content := model.Content{
				Name:              dealRequest.Cid,
				Size:              dealRequest.Size,
				Cid:               dealRequest.Cid,
				RequestingApiKey:  authParts[1],
				PieceCommitmentId: pieceCommp.ID,
				Status:            utils.CONTENT_DEAL_MAKING_PROPOSAL,
				ConnectionMode:    connMode,
				CreatedAt:         time.Now(),
				UpdatedAt:         time.Now(),
			}
			tx.Create(&content)
			dealRequest.Cid = content.Cid

			//	assign a miner
			if dealRequest.Miner != "" {
				contentMinerAssignment := model.ContentMiner{
					Miner:     dealRequest.Miner,
					Content:   content.ID,
					CreatedAt: time.Now(),
					UpdatedAt: time.Now(),
				}
				tx.Create(&contentMinerAssignment)
				dealRequest.Miner = contentMinerAssignment.Miner
			}

			// 	assign a wallet_estuary
			if (WalletRequest{} != dealRequest.Wallet) {

				// get wallet from wallets database
				var wallet model.Wallet
				if dealRequest.Wallet.Address != "" {
					tx.Where("addr = ? and owner = ?", dealRequest.Wallet.Address, authParts[1]).First(&wallet)
				} else if dealRequest.Wallet.Uuid != "" {
					tx.Where("uu_id = ? and owner = ?", dealRequest.Wallet.Uuid, authParts[1]).First(&wallet)
				} else {
					tx.Where("id = ? and owner = ?", dealRequest.Wallet.Id, authParts[1]).First(&wallet)
				}

				if wallet.ID == 0 {
					tx.Rollback()
					return errors.New("Wallet not found, please make sure the wallet is registered with the API key " + dealRequest.Wallet.Address)
				}

				// create the wallet request object
				var hexedWallet WalletRequest
				hexedWallet.KeyType = wallet.KeyType
				hexedWallet.PrivateKey = wallet.PrivateKey

				if err != nil {
					tx.Rollback()
					return errors.New("Error encoding the wallet")
				}

				// assign the wallet to the content
				contentWalletAssignment := model.ContentWallet{
					WalletId:  wallet.ID,
					Content:   content.ID,
					CreatedAt: time.Now(),
					UpdatedAt: time.Now(),
				}
				tx.Create(&contentWalletAssignment)

				dealRequest.Wallet = WalletRequest{
					Id:      dealRequest.Wallet.Id,
					Address: wallet.Addr,
				}
			}

			var dealProposalParam model.ContentDealProposalParameters
			dealProposalParam.CreatedAt = time.Now()
			dealProposalParam.UpdatedAt = time.Now()
			dealProposalParam.Content = content.ID
			dealProposalParam.Label = func() string {
				if dealRequest.Label != "" {
					return dealRequest.Label
				}
				return content.Cid
			}()
			dealProposalParam.SkipIPNIAnnounce = dealRequest.SkipIPNIAnnounce
			dealProposalParam.VerifiedDeal = func() bool {
				if dealRequest.DealVerifyState == utils.DEAL_VERIFIED {
					return true
				}
				return false
			}()
			// start epoch
			if dealRequest.StartEpoch != 0 {
				dealProposalParam.StartEpoch = dealRequest.StartEpoch
			}
			// duration
			if dealRequest.Duration == 0 {
				dealProposalParam.Duration = utils.DEFAULT_DURATION
			} else {
				dealProposalParam.Duration = dealRequest.Duration
			}

			if dealRequest.StartEpochInDays != 0 {
				startEpochTime := time.Now().AddDate(0, 0, int(dealRequest.StartEpochInDays))
				dealRequest.StartEpoch = utils.DateToHeight(startEpochTime)
				dealRequest.StartEpoch = dealRequest.StartEpoch + (utils.EPOCH_PER_HOUR * 24 * 7)
				dealProposalParam.StartEpoch = dealRequest.StartEpoch
			}

			if dealRequest.DurationInDays > 540 {
				return errors.New("Duration cannot be more than 540 days")
			}

			if dealRequest.DurationInDays != 0 {
				dealProposalParam.Duration = utils.EPOCH_PER_DAY * (dealRequest.DurationInDays - 7)
			}

			// remove unsealed copy
			if dealRequest.RemoveUnsealedCopies == false {
				dealProposalParam.RemoveUnsealedCopy = false
			} else {
				dealProposalParam.RemoveUnsealedCopy = true
			}

			// deal proposal parameters
			tx.Create(&dealProposalParam)

			var dispatchJobs core.IProcessor
			if pieceCommp.ID != 0 {
				dispatchJobs = jobs.NewStorageDealMakerProcessor(node, content, pieceCommp) // straight to storage deal making
			}

			node.Dispatcher.AddJob(dispatchJobs)

			dealResponses = append(dealResponses, DealResponse{
				Status:                       "success",
				Message:                      "Deal request received. Please take note of the content_id",
				ContentId:                    content.ID,
				DealRequest:                  dealRequest,
				DealProposalParameterRequest: dealProposalParam,
			})

		}
		node.Dispatcher.Start(len(dealRequests))
		err = c.JSON(http.StatusOK, dealResponses)
		if err != nil {
			tx.Rollback()
			return errors.New("Error sending the response" + err.Error())
		}
		return nil
	})
	if errTxn != nil {
		return errors.New("Error in making a deal proposal " + errTxn.Error())
	}
	return nil
}

// It takes a contentId as a parameter, looks up the status of the content, and returns the status as JSON
func handleContentStats(c echo.Context, statsService core.StatsService) error {
	contentIdParam := c.Param("contentId")
	contentId, err := strconv.Atoi(contentIdParam)
	if err != nil {
		return errors.New("Error looking up the status of the content" + err.Error())
	}

	status, err := statsService.ContentStatus(core.ContentStatsParam{
		ContentId: int64(contentId),
	})

	if err != nil {
		return errors.New("Error looking up the status of the content" + err.Error())

	}

	return c.JSON(200, status)
}

// It takes a piece commitment ID, looks up the status of the piece commitment, and returns the status
func handleCommitmentPieceStats(c echo.Context, statsService core.StatsService) error {
	pieceCommitmentIdParam := c.Param("piece-commitmentId")
	pieceCommitmentId, err := strconv.Atoi(pieceCommitmentIdParam)
	if err != nil {
		return errors.New("Error looking up the status of the piece commitment" + err.Error())
	}

	status, err := statsService.PieceCommitmentStatus(core.PieceCommitmentStatsParam{
		PieceCommpId: int64(pieceCommitmentId),
	})

	if err != nil {
		return errors.New("Error looking up the status of the piece commitment" + err.Error())
	}

	return c.JSON(200, status)
}

type ValidateMetaResult struct {
	IsValid bool
	Message string
}

// `ValidateMeta` validates the `DealRequest` struct and returns an error if the request is invalid
func ValidatePieceCommitmentMeta(pieceCommitmentRequest PieceCommitmentRequest) error {
	if (PieceCommitmentRequest{} == pieceCommitmentRequest) {
		return errors.New("invalid piece_commitment request. piece_commitment is required")
	}

	return nil
}

// It validates the deal request and returns an error if the request is invalid
func ValidateMeta(dealRequest DealRequest) error {

	if (DealRequest{} == dealRequest) {
		return errors.New("invalid deal request")
	}
	// miner is required
	if (DealRequest{} != dealRequest && dealRequest.Miner == "") {
		return errors.New("miner is required")
	}

	if (DealRequest{} != dealRequest && dealRequest.DurationInDays > 0 && dealRequest.StartEpochInDays == 0) {
		return errors.New("start_epoch_in_days is required when duration_in_days is set")
	}

	if (DealRequest{} != dealRequest && dealRequest.StartEpochInDays > 0 && dealRequest.DurationInDays == 0) {
		return errors.New("duration_in_days is required when start_epoch_in_days is set")
	}

	if (DealRequest{} != dealRequest && dealRequest.StartEpochInDays > 14) {
		return errors.New("start_epoch_in_days can only be 14 days or less")
	}

	if (DealRequest{} != dealRequest && dealRequest.Replication > 6) {
		return errors.New("replication count is more than allowed (6)")
	}

	// label length must be less than 100
	if (DealRequest{} != dealRequest && len(dealRequest.Label) > 100) {
		return errors.New("label length must be less than 100")
	}

	if (DealRequest{} != dealRequest && dealRequest.DealVerifyState == "") {
		dealRequest.DealVerifyState = utils.DEAL_VERIFIED
	} else if (DealRequest{} != dealRequest && dealRequest.DealVerifyState != utils.DEAL_VERIFIED) {
		dealRequest.DealVerifyState = utils.DEAL_UNVERIFIED
	}

	// connection mode is required
	if (DealRequest{} != dealRequest) {
		switch dealRequest.ConnectionMode {
		case "":
			dealRequest.ConnectionMode = utils.CONNECTION_MODE_E2E
		case utils.CONNECTION_MODE_E2E:
		case utils.CONNECTION_MODE_IMPORT:
		default:
			return errors.New("connection mode can only be e2e or import")
		}
	}

	// piece commitment is required
	if (PieceCommitmentRequest{} != dealRequest.PieceCommitment && dealRequest.PieceCommitment.Piece == "") {
		return errors.New("piece commitment is invalid, make sure you have the cid, piece_cid, size and padded_piece_size or unpadded_piece_size")
	}

	if (PieceCommitmentRequest{} != dealRequest.PieceCommitment && dealRequest.PieceCommitment.Piece != "") &&
		(dealRequest.PieceCommitment.PaddedPieceSize == 0 && dealRequest.PieceCommitment.UnPaddedPieceSize == 0) &&
		(dealRequest.Size == 0) {
		return errors.New("piece commitment is invalid, make sure you have the cid, piece_cid, size and padded_piece_size or unpadded_piece_size")

	}

	if (WalletRequest{} != dealRequest.Wallet && dealRequest.Wallet.Address == "") {
		return errors.New("wallet address is required")
	}

	// catch any panics
	defer func() {
		if r := recover(); r != nil {
			fmt.Println("Recovered in f", r)
		}
	}()
	return nil
}

// It takes a request, and returns a response
func handlePrepareContent(c echo.Context, node *core.DeltaNode, statsService core.StatsService) {
	// > This function is called when a node receives a `PrepareCommitmentPiece` message
}
func handlePrepareCommitmentPiece() {
	// > This function is called when the user clicks the "Prepare Commitment Pieces" button. It takes the user's input and
	// prepares the commitment pieces
}
func handlePrepareCommitmentPieces() {
	// This function handles the announcement of content.
}

// This function is called when the user clicks the "Announce Content" button. It takes the user's input and...
func handleAnnounceContent() {
	//	> This function is called when the user clicks the "Announce Content" button. It takes the user's input and
}

// > The function `handleAnnounceCommitmentPiece` is called when a `AnnounceCommitmentPiece` message is received
func handleAnnounceCommitmentPiece() {

}

// > This function is called when a commitment piece is received from a peer

func handleAnnounceCommitmentPieces() {

}

func handleRequest(c echo.Context, node *core.DeltaNode, dealRequest DealRequest, addNode ipld.Node, file *multipart.FileHeader, authParts []string) error {
	var err error
	errTxn := node.DB.Transaction(func(tx *gorm.DB) error {

		// let's create a commp but only if we have
		// a cid, a piece_cid, a padded_piece_size, size
		var pieceCommp model.PieceCommitment
		if (PieceCommitmentRequest{} != dealRequest.PieceCommitment && dealRequest.PieceCommitment.Piece != "") &&
			(dealRequest.PieceCommitment.PaddedPieceSize != 0) &&
			(dealRequest.Size != 0) {

			// if commp is there, make sure the piece and size are there. Use default duration.
			pieceCommp.Cid = addNode.Cid().String()
			pieceCommp.Piece = dealRequest.PieceCommitment.Piece
			pieceCommp.Size = file.Size
			pieceCommp.UnPaddedPieceSize = dealRequest.PieceCommitment.UnPaddedPieceSize
			pieceCommp.PaddedPieceSize = dealRequest.PieceCommitment.PaddedPieceSize
			pieceCommp.CreatedAt = time.Now()
			pieceCommp.UpdatedAt = time.Now()
			pieceCommp.Status = utils.COMMP_STATUS_OPEN
			node.DB.Create(&pieceCommp)

			dealRequest.PieceCommitment = PieceCommitmentRequest{
				Piece:             pieceCommp.Piece,
				PaddedPieceSize:   pieceCommp.PaddedPieceSize,
				UnPaddedPieceSize: pieceCommp.UnPaddedPieceSize,
			}
		}

		// save the content to the DB with the piece_commitment_id
		content := model.Content{
			Name:              file.Filename,
			Size:              file.Size,
			Cid:               addNode.Cid().String(),
			RequestingApiKey:  authParts[1],
			PieceCommitmentId: pieceCommp.ID,
			Status:            utils.CONTENT_PINNED,
			ConnectionMode:    dealRequest.ConnectionMode,
			CreatedAt:         time.Now(),
			UpdatedAt:         time.Now(),
		}
		node.DB.Create(&content)
		dealRequest.Cid = content.Cid

		//	assign a miner
		if dealRequest.Miner != "" {
			contentMinerAssignment := model.ContentMiner{
				Miner:     dealRequest.Miner,
				Content:   content.ID,
				CreatedAt: time.Now(),
				UpdatedAt: time.Now(),
			}
			node.DB.Create(&contentMinerAssignment)
			dealRequest.Miner = contentMinerAssignment.Miner
		}

		if (WalletRequest{} != dealRequest.Wallet) {

			// get wallet from wallets database
			var wallet model.Wallet

			if dealRequest.Wallet.Address != "" {
				node.DB.Where("addr = ? and owner = ?", dealRequest.Wallet.Address, authParts[1]).First(&wallet)
			} else if dealRequest.Wallet.Uuid != "" {
				node.DB.Where("uuid = ? and owner = ?", dealRequest.Wallet.Uuid, authParts[1]).First(&wallet)
			} else {
				node.DB.Where("id = ? and owner = ?", dealRequest.Wallet.Id, authParts[1]).First(&wallet)
			}

			if wallet.ID == 0 {
				return errors.New("Wallet not found, please make sure the wallet is registered")
			}

			// create the wallet request object
			var hexedWallet WalletRequest
			hexedWallet.KeyType = wallet.KeyType
			hexedWallet.PrivateKey = wallet.PrivateKey

			if err != nil {
				return errors.New("Error encoding the wallet")
			}

			// assign the wallet to the content
			contentWalletAssignment := model.ContentWallet{
				WalletId:  wallet.ID,
				Content:   content.ID,
				CreatedAt: time.Now(),
				UpdatedAt: time.Now(),
			}
			node.DB.Create(&contentWalletAssignment)

			dealRequest.Wallet = WalletRequest{
				Id:      dealRequest.Wallet.Id,
				Address: wallet.Addr,
			}
		}

		var dealProposalParam model.ContentDealProposalParameters
		dealProposalParam.CreatedAt = time.Now()
		dealProposalParam.UpdatedAt = time.Now()
		dealProposalParam.Content = content.ID
		dealProposalParam.Label = func() string {
			if dealRequest.Label != "" {
				return dealRequest.Label
			}
			return content.Cid
		}()
		dealProposalParam.SkipIPNIAnnounce = dealRequest.SkipIPNIAnnounce
		dealProposalParam.VerifiedDeal = func() bool {
			if dealRequest.DealVerifyState == utils.DEAL_VERIFIED {
				return true
			}
			return false
		}()
		// start epoch
		if dealRequest.StartEpoch != 0 {
			dealProposalParam.StartEpoch = dealRequest.StartEpoch
		}
		// duration
		if dealRequest.Duration == 0 {
			dealProposalParam.Duration = utils.DEFAULT_DURATION
		} else {
			dealProposalParam.Duration = dealRequest.Duration
		}

		if dealRequest.StartEpochInDays != 0 {
			startEpochTime := time.Now().AddDate(0, 0, int(dealRequest.StartEpochInDays))
			dealRequest.StartEpoch = utils.DateToHeight(startEpochTime)
			dealRequest.StartEpoch = dealRequest.StartEpoch + (utils.EPOCH_PER_HOUR * 24 * 7)
		}

		if dealRequest.DurationInDays > 540 {
			return errors.New("Duration cannot be more than 540 days")
		}

		if dealRequest.DurationInDays != 0 {
			dealProposalParam.Duration = utils.EPOCH_PER_DAY * (dealRequest.DurationInDays - 7)
		}

		// remove unsealed copy
		if dealRequest.RemoveUnsealedCopies == false {
			dealProposalParam.RemoveUnsealedCopy = false
		} else {
			dealProposalParam.RemoveUnsealedCopy = true
		}

		// deal proposal parameters
		node.DB.Create(&dealProposalParam)

		var dispatchJobs core.IProcessor
		if pieceCommp.ID != 0 {
			dispatchJobs = jobs.NewStorageDealMakerProcessor(node, content, pieceCommp) // straight to storage deal making
		} else {
			dispatchJobs = jobs.NewPieceCommpProcessor(node, content) // straight to pieceCommp
		}

		node.Dispatcher.AddJobAndDispatch(dispatchJobs, 1)

		err = c.JSON(200, DealResponse{
			Status:                       "success",
			Message:                      "Deal request received. Please take note of the content_id. You can use the content_id to check the status of the deal.",
			ContentId:                    content.ID,
			DealRequest:                  dealRequest,
			DealProposalParameterRequest: dealProposalParam,
		})
		if err != nil {
			return err
		}

		// return transaction
		return nil
	})
	if errTxn != nil {
		return errors.New("Error creating the content record" + " " + errTxn.Error())
	}
	return nil
}
