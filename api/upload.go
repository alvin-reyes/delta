package api

import (
	"delta/core"
	"delta/jobs"
	"delta/utils"
	"encoding/json"
	"fmt"
	"strconv"
	"strings"
	"time"

	"github.com/ipfs/go-cid"
	"github.com/labstack/echo/v4"
)

type CidRequest struct {
	Cids []string `json:"cids"`
}

type UploadResponse struct {
	Status  string `json:"status"`
	Message string `json:"message"`
	ID      int64  `json:"id,omitempty"`
}

type UploadCommpRequest struct {
	Cid        string `json:"cid"`
	Piece      string `json:"piece"`
	Size       int64  `json:"size"`
	PaddedSize int64  `json:"padded_size"`
}

type WalletRequest struct {
	KeyType    string `json:"key_type"`
	PrivateKey string `json:"private_key"`
}

func ConfigureUploadRouter(e *echo.Group, node *core.DeltaNode) {

	content := e.Group("/content")

	content.POST("/add", func(c echo.Context) error {
		authorizationString := c.Request().Header.Get("Authorization")
		authParts := strings.Split(authorizationString, " ")
		file, err := c.FormFile("data")
		connMode := c.FormValue("connection_mode") // online or offline
		miner := c.FormValue("miner")
		duration := c.FormValue("duration")
		size := c.FormValue("size")
		walletReq := WalletRequest{}
		//{"Type":"secp256k1","PrivateKey":"<key>"}
		walletAddr := c.FormValue("wallet") // insecure
		fmt.Println(duration)
		json.Unmarshal([]byte(walletAddr), &walletReq)

		if connMode == "" || (connMode != utils.CONNECTION_MODE_ONLINE && connMode != utils.CONNECTION_MODE_OFFLINE) {
			connMode = "online"
		}

		if err != nil {
			return err
		}

		src, err := file.Open()
		if err != nil {
			return err
		}

		addNode, err := node.Node.AddPinFile(c.Request().Context(), src, nil)

		// if size is given, let's create a commp record for it.
		var pieceCommp core.PieceCommitment
		if size != "" {
			paddedPieceSize, err := strconv.Atoi(size)
			if err != nil {
				return err
			}
			pieceCommp.Cid = addNode.Cid().String()
			pieceCommp.Piece = addNode.Cid().String()
			pieceCommp.Size = file.Size
			pieceCommp.PaddedPieceSize = int64(paddedPieceSize)
			pieceCommp.CreatedAt = time.Now()
			pieceCommp.UpdatedAt = time.Now()
			pieceCommp.Status = utils.COMMP_STATUS_OPEN
			node.DB.Create(&pieceCommp)
		}

		// save the file to the database.
		content := core.Content{
			Name:              file.Filename,
			Size:              file.Size,
			Cid:               addNode.Cid().String(),
			RequestingApiKey:  authParts[1],
			PieceCommitmentId: pieceCommp.ID,
			Status:            utils.CONTENT_PINNED,
			ConnectionMode:    connMode,
			CreatedAt:         time.Now(),
			UpdatedAt:         time.Now(),
		}
		node.DB.Create(&content)

		//	assign a miner
		if miner != "" {
			contentMinerAssignment := core.ContentMinerAssignment{
				Miner:     miner,
				Content:   content.ID,
				CreatedAt: time.Now(),
				UpdatedAt: time.Now(),
			}
			node.DB.Create(&contentMinerAssignment)
		}

		//  create a wallet record

		// 	assign a wallet
		if walletAddr != "" {

			contentWalletAssignment := core.ContentWalletAssignment{
				Wallet:    walletAddr,
				Content:   content.ID,
				CreatedAt: time.Now(),
				UpdatedAt: time.Now(),
			}
			node.DB.Create(&contentWalletAssignment)
		}

		//	error
		if err != nil {
			c.JSON(500, UploadResponse{
				Status:  "error",
				Message: "Error pinning the file" + err.Error(),
			})
		}

		c.JSON(200, UploadResponse{
			Status:  "success",
			Message: "File uploaded and pinned successfully",
			ID:      content.ID,
		})

		var dispatchJobs core.IProcessor
		if pieceCommp.ID != 0 {
			dispatchJobs = jobs.NewStorageDealMakerProcessor(node, content, pieceCommp) // straight to storage deal making
			// add the job so we can process it later
		} else {
			dispatchJobs = jobs.NewPieceCommpProcessor(node, content) // straight to pieceCommp
			// add the job so we can process it later
		}

		node.Dispatcher.AddJob(dispatchJobs)

		return nil
	})

	content.POST("/commp", func(c echo.Context) error {
		var req UploadCommpRequest
		c.Bind(&req)
		return nil
	})

	content.POST("/commps", func(c echo.Context) error {
		var req []UploadCommpRequest
		c.Bind(&req)

		for _, r := range req {
			var pieceCommp core.PieceCommitment
			pieceCommp.Cid = r.Cid
			pieceCommp.Piece = r.Piece
			pieceCommp.Size = r.Size
			pieceCommp.PaddedPieceSize = r.PaddedSize
			pieceCommp.CreatedAt = time.Now()
			pieceCommp.UpdatedAt = time.Now()
			pieceCommp.Status = utils.COMMP_STATUS_OPEN
			node.DB.Create(&pieceCommp)

			//jobs.NewStorageDealMakerProcessor(node, nil, pieceCommp)
		}
		return nil
	})

	content.POST("/cid/:cid", func(c echo.Context) error {
		authorizationString := c.Request().Header.Get("Authorization")
		authParts := strings.Split(authorizationString, " ")
		cidFromForm := c.Param("cid")
		cidNode, err := cid.Decode(cidFromForm)
		if err != nil {
			return err
		}

		//	 get the node
		addNode, err := node.Node.Get(c.Request().Context(), cidNode)

		// get available staging buckets.
		// save the file to the database.
		size, err := addNode.Size()

		content := core.Content{
			Name:             addNode.Cid().String(),
			Size:             int64(size),
			Cid:              addNode.Cid().String(),
			RequestingApiKey: authParts[1],
			Status:           utils.CONTENT_PINNED,
			CreatedAt:        time.Now(),
			UpdatedAt:        time.Now(),
		}

		node.DB.Create(&content)

		if err != nil {
			c.JSON(500, UploadResponse{
				Status:  "error",
				Message: "Error pinning the cid" + err.Error(),
			})
		}

		c.JSON(200, UploadResponse{
			Status:  "success",
			Message: "CID uploaded and pinned successfully",
			ID:      content.ID,
		})

		d := jobs.NewPieceCommpProcessor(node, content)
		node.Dispatcher.AddJob(d) // add the job so we can process it later

		return nil
	})

	content.POST("/cids", func(c echo.Context) error {
		authorizationString := c.Request().Header.Get("Authorization")
		authParts := strings.Split(authorizationString, " ")
		var cidRequest CidRequest
		c.Bind(&cidRequest)
		for _, cidFromForm := range cidRequest.Cids {
			cidNode, err := cid.Decode(cidFromForm)
			if err != nil {
				return err
			}

			//	 get the node and save on the database
			addNode, err := node.Node.Get(c.Request().Context(), cidNode)

			// get available staging buckets.
			// save the file to the database.
			size, err := addNode.Size()

			content := core.Content{
				Name:             addNode.Cid().String(),
				Size:             int64(size),
				Cid:              addNode.Cid().String(),
				RequestingApiKey: authParts[1],
				Status:           utils.CONTENT_PINNED,
				CreatedAt:        time.Now(),
				UpdatedAt:        time.Now(),
			}

			node.DB.Create(&content)
			d := jobs.NewPieceCommpProcessor(node, content)
			node.Dispatcher.AddJob(d) // add the job so we can process it later
		}
		return nil
	})
}
