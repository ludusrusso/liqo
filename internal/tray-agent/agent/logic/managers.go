package logic

import (
	"github.com/liqotech/liqo/internal/tray-agent/agent/client"
	app "github.com/liqotech/liqo/internal/tray-agent/app-indicator"
	"github.com/skratchdot/open-golang/open"
)

//OnReady is the routine orchestrating Liqo Agent execution.
func OnReady() {
	// Indicator configuration
	i := app.GetIndicator()
	i.RefreshStatus()
	startListenerAdvertisements(i)
	startQuickOnOff(i)
	startQuickChangeMode(i)
	startQuickDashboard(i)
	startQuickSetNotifications(i)
	startQuickLiqoWebsite(i)
	startQuickQuit(i)
	//try to start Liqo and main ACTION
	quickTurnOnOff(i)
}

//OnExit is the routine containing clean-up operations to be performed at Liqo Agent exit.
func OnExit() {
	app.GetIndicator().Disconnect()
}

//startQuickOnOff is the wrapper function to register the QUICK "START/STOP LIQO".
func startQuickOnOff(i *app.Indicator) {
	i.AddQuick("", qOnOff, func(args ...interface{}) {
		quickTurnOnOff(args[0].(*app.Indicator))
	}, i)
	//the Quick MenuNode title is refreshed
	updateQuickTurnOnOff(i)
}

//startQuickChangeMode is the wrapper function to register the QUICK "CHANGE LIQO MODE"
func startQuickChangeMode(i *app.Indicator) {
	i.AddQuick("", qMode, func(args ...interface{}) {
		quickChangeMode(i)
	}, i)
	//the Quick MenuNode title is refreshed
	updateQuickChangeMode(i)
}

//startQuickLiqoWebsite is the wrapper function to register QUICK "About Liqo".
func startQuickLiqoWebsite(i *app.Indicator) {
	i.AddQuick("ⓘ ABOUT LIQO", qWeb, func(args ...interface{}) {
		_ = open.Start("http://liqo.io")
	})
}

//startQuickDashboard is the wrapper function to register QUICK "LAUNCH Liqo Dash".
func startQuickDashboard(i *app.Indicator) {
	i.AddQuick("LIQODASH", qDash, func(args ...interface{}) {
		quickConnectDashboard(i)
	})
}

//startQuickSetNotifications is the wrapper function to register QUICK "Change Notification settings".
func startQuickSetNotifications(i *app.Indicator) {
	i.AddQuick("NOTIFICATIONS SETTINGS", qNotify, func(args ...interface{}) {
		quickChangeNotifyLevel()
	})
}

//startQuickQuit is the wrapper function to register QUICK "QUIT".
func startQuickQuit(i *app.Indicator) {
	i.AddQuick("QUIT", qQuit, func(args ...interface{}) {
		i := args[0].(*app.Indicator)
		i.Quit()
	}, i)
}

//LISTENERS

// wrapper that starts the Listeners for the events regarding the Advertisement CRD
func startListenerAdvertisements(i *app.Indicator) {
	i.Listen(client.ChanAdvNew, i.AgentCtrl().NotifyChannel(client.ChanAdvNew), func(objName string, args ...interface{}) {
		ctrl := i.AgentCtrl()
		if !ctrl.Mocked() {
			advStore := ctrl.Controller(client.CRAdvertisement).Store
			_, exist, err := advStore.GetByKey(objName)
			if err != nil {
				i.NotifyNoConnection()
				return
			}
			if !exist {
				return
			}
		}
		i.NotifyNewAdv(objName)
	})
	i.Listen(client.ChanAdvAccepted, i.AgentCtrl().NotifyChannel(client.ChanAdvAccepted), func(objName string, args ...interface{}) {
		ctrl := i.AgentCtrl()
		if !ctrl.Mocked() {
			advStore := ctrl.Controller(client.CRAdvertisement).Store
			_, exist, err := advStore.GetByKey(objName)
			if err != nil {
				i.NotifyNoConnection()
				return
			}
			if !exist {
				return
			}
		}
		i.NotifyAcceptedAdv(objName)
		i.Status().IncConsumePeerings()
	})
	i.Listen(client.ChanAdvRevoked, i.AgentCtrl().NotifyChannel(client.ChanAdvRevoked), func(objName string, args ...interface{}) {
		ctrl := i.AgentCtrl()
		if !ctrl.Mocked() {
			advStore := ctrl.Controller(client.CRAdvertisement).Store
			_, exist, err := advStore.GetByKey(objName)
			if err != nil {
				i.NotifyNoConnection()
				return
			}
			if !exist {
				return
			}
		}
		i.NotifyRevokedAdv(objName)
		i.Status().DecConsumePeerings()
	})
	i.Listen(client.ChanAdvDeleted, i.AgentCtrl().NotifyChannel(client.ChanAdvDeleted), func(objName string, args ...interface{}) {
		i.NotifyDeletedAdv(objName)
		i.Status().DecConsumePeerings()
	})
}
