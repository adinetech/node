package command_run

import (
	"github.com/mysterium/node/communication"
	"github.com/mysterium/node/identity"
	"github.com/mysterium/node/ipify"
	"github.com/mysterium/node/nat"
	"github.com/mysterium/node/openvpn"
	"github.com/mysterium/node/openvpn/service_discovery"
	vpn_session "github.com/mysterium/node/openvpn/session"
	"github.com/mysterium/node/server"
	dto_server "github.com/mysterium/node/server/dto"
	dto_discovery "github.com/mysterium/node/service_discovery/dto"
	"github.com/mysterium/node/session"
	"io"
	"time"
)

type CommandRun struct {
	Output      io.Writer
	OutputError io.Writer

	IdentitySelector func() (identity.Identity, error)
	IpifyClient      ipify.Client
	MysteriumClient  server.Client
	NatService       nat.NATService

	DialogWaiterFactory func(identity identity.Identity) (communication.DialogWaiter, dto_discovery.Contact)
	dialogWaiter        communication.DialogWaiter

	SessionManager session.ManagerInterface

	VpnServerFactory func() *openvpn.Server
	vpnServer        *openvpn.Server
}

func (cmd *CommandRun) Run(options CommandOptions) (err error) {
	providerId, err := cmd.IdentitySelector()
	if err != nil {
		return err
	}

	var providerContact dto_discovery.Contact
	cmd.dialogWaiter, providerContact = cmd.DialogWaiterFactory(providerId)

	// if for some reason we will need truly external IP, use GetPublicIP()
	vpnServerIp, err := cmd.IpifyClient.GetOutboundIP()
	if err != nil {
		return err
	}

	cmd.NatService.Add(nat.RuleForwarding{
		SourceAddress: "10.8.0.0/24",
		TargetIp:      vpnServerIp,
	})

	// clear probable stale entries
	cmd.NatService.Stop()

	if err = cmd.NatService.Start(); err != nil {
		return err
	}

	proposal := service_discovery.NewServiceProposal(providerId, providerContact)

	sessionCreateConsumer := &vpn_session.SessionCreateConsumer{
		CurrentProposalId: proposal.Id,
		SessionManager:    cmd.SessionManager,
		ClientConfigFactory: func() *openvpn.ClientConfig {
			return openvpn.NewClientConfig(
				vpnServerIp,
				options.DirectoryConfig+"/ca.crt",
				options.DirectoryConfig+"/client.crt",
				options.DirectoryConfig+"/client.key",
				options.DirectoryConfig+"/ta.key",
			)
		},
	}
	if err = cmd.dialogWaiter.ServeDialogs(sessionCreateConsumer); err != nil {
		return err
	}

	cmd.vpnServer = cmd.VpnServerFactory()
	if err := cmd.vpnServer.Start(); err != nil {
		return err
	}

	if err := cmd.MysteriumClient.NodeRegister(proposal); err != nil {
		return err
	}
	go func() {
		for {
			time.Sleep(1 * time.Minute)
			cmd.MysteriumClient.NodeSendStats(providerId.Address, []dto_server.SessionStatsDeprecated{})
		}
	}()

	return nil
}

func (cmd *CommandRun) Wait() error {
	return cmd.vpnServer.Wait()
}

func (cmd *CommandRun) Kill() {
	cmd.vpnServer.Stop()
	cmd.dialogWaiter.Stop()
	cmd.NatService.Stop()
}
