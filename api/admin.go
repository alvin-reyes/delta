package api

import (
	"delta/core"
	"github.com/labstack/echo/v4"
)

func ConfigureAdminRouter(e *echo.Group, node *core.DeltaNode) {

	adminRepair := e.Group("/repair")
	adminWallet := e.Group("/wallet")
	adminDashboard := e.Group("/dashboard")

	// repair endpoints
	adminRepair.GET("/deal", func(c echo.Context) error {
		return nil
	})

	adminRepair.GET("/commp", func(c echo.Context) error {
		return nil
	})

	adminRepair.GET("/run-cleanup", func(c echo.Context) error {
		return nil
	})

	adminRepair.GET("/retry-deal-making-content", func(c echo.Context) error {
		return nil
	})

	// add wallet endpoint
	adminWallet.POST("/add", func(c echo.Context) error {
		return nil
	})

	adminWallet.POST("/import", func(c echo.Context) error {
		return nil
	})

	// list wallet endpoint
	adminWallet.GET("/list", func(c echo.Context) error {
		return nil
	})

	adminWallet.GET("/info", func(c echo.Context) error {
		return nil
	})

	adminDashboard.GET("/index", func(c echo.Context) error {
		return nil
	})
}