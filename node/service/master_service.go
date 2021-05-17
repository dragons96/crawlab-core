package service

import (
	"github.com/apex/log"
	"github.com/crawlab-team/crawlab-core/constants"
	"github.com/crawlab-team/crawlab-core/errors"
	"github.com/crawlab-team/crawlab-core/grpc/server"
	"github.com/crawlab-team/crawlab-core/interfaces"
	"github.com/crawlab-team/crawlab-core/models/delegate"
	"github.com/crawlab-team/crawlab-core/models/models"
	"github.com/crawlab-team/crawlab-core/models/service"
	"github.com/crawlab-team/crawlab-core/node/config"
	"github.com/crawlab-team/crawlab-core/utils"
	grpc "github.com/crawlab-team/crawlab-grpc"
	"github.com/crawlab-team/go-trace"
	"go.mongodb.org/mongo-driver/bson"
	mongo2 "go.mongodb.org/mongo-driver/mongo"
	"go.uber.org/dig"
	"time"
)

type MasterService struct {
	modelSvc service.ModelService
	cfgSvc   interfaces.NodeConfigService
	server   interfaces.GrpcServer

	// settings
	cfgPath         string
	address         interfaces.Address
	monitorInterval time.Duration
	stopOnError     bool
}

func (svc *MasterService) Init() (err error) {
	// do nothing
	return nil
}

func (svc *MasterService) Start() {
	// start grpc server
	if err := svc.server.Start(); err != nil {
		panic(err)
	}

	// register to db
	if err := svc.Register(); err != nil {
		panic(err)
	}

	// start monitoring worker nodes
	go svc.Monitor()

	// wait for quit signal
	svc.Wait()

	// stop
	svc.Stop()
}

func (svc *MasterService) Wait() {
	utils.DefaultWait()
}

func (svc *MasterService) Stop() {
	_ = svc.server.Stop()
	log.Infof("master[%s] service has stopped", svc.GetConfigService().GetNodeKey())
}

func (svc *MasterService) Monitor() {
	for {
		if err := svc.monitor(); err != nil {
			trace.PrintError(err)
			if svc.stopOnError {
				svc.Stop()
				return
			}
		}

		time.Sleep(svc.monitorInterval)
	}
}

func (svc *MasterService) GetConfigService() (cfgSvc interfaces.NodeConfigService) {
	return svc.cfgSvc
}

func (svc *MasterService) GetConfigPath() (path string) {
	return svc.cfgPath
}

func (svc *MasterService) SetConfigPath(path string) {
	svc.cfgPath = path
}

func (svc *MasterService) GetAddress() (address interfaces.Address) {
	return svc.address
}

func (svc *MasterService) SetAddress(address interfaces.Address) {
	svc.address = address
}

func (svc *MasterService) SetMonitorInterval(duration time.Duration) {
	svc.monitorInterval = duration
}

func (svc *MasterService) Register() (err error) {
	nodeKey := svc.GetConfigService().GetNodeKey()
	node, err := svc.modelSvc.GetNodeByKey(nodeKey, nil)
	if err != nil && err.Error() == mongo2.ErrNoDocuments.Error() {
		// not exists
		log.Infof("master[%s] does not exist in db", nodeKey)
		node := &models.Node{
			Key:      nodeKey,
			Name:     nodeKey,
			IsMaster: true,
			Status:   constants.NodeStatusOnline,
			Enabled:  true,
			Active:   true,
			ActiveTs: time.Now(),
		}
		nodeD := delegate.NewModelNodeDelegate(node)
		if err := nodeD.Add(); err != nil {
			return err
		}
		log.Infof("added master[%s] in db. id: %s", nodeKey, nodeD.GetModel().GetId().Hex())
		return nil
	} else if err == nil {
		// exists
		log.Infof("master[%s] exists in db", nodeKey)
		nodeD := delegate.NewModelNodeDelegate(node)
		if err := nodeD.UpdateStatusOnline(); err != nil {
			return err
		}
		log.Infof("updated master[%s] in db. id: %s", nodeKey, nodeD.GetModel().GetId().Hex())
		return nil
	} else {
		// error
		return err
	}
}

func (svc *MasterService) StopOnError() {
	svc.stopOnError = true
}

func (svc *MasterService) GetServer() (svr interfaces.GrpcServer) {
	return svc.server
}

func (svc *MasterService) monitor() (err error) {
	// update master node status in db
	if err := svc.updateMasterNodeStatus(); err != nil {
		return err
	}

	// all worker nodes
	nodes, err := svc.modelSvc.GetNodeList(bson.M{"is_master": false}, nil)
	if err != nil {
		if err == mongo2.ErrNoDocuments {
			return nil
		}
		return trace.TraceError(err)
	}

	// error flag
	isErr := false

	// iterate all nodes
	for _, n := range nodes {
		// subscribe
		sub, err := svc.server.GetSubscribe(n.GetKey())
		if err != nil {
			trace.PrintError(err)
			isErr = true
			if err := svc.setWorkerNodeOffline(&n); err != nil {
				trace.PrintError(err)
			}
			continue
		}

		// PING client
		if err := sub.GetStream().Send(&grpc.StreamMessage{
			Code:    grpc.StreamMessageCode_PING,
			NodeKey: svc.GetConfigService().GetNodeKey(),
		}); err != nil {
			log.Errorf("cannot ping worker[%s]: %v", n.GetKey(), err)
			isErr = true
			if err := svc.setWorkerNodeOffline(&n); err != nil {
				trace.PrintError(err)
			}
			continue
		}
	}

	if isErr {
		return trace.TraceError(errors.ErrorNodeMonitorError)
	}

	return nil
}

func (svc *MasterService) updateMasterNodeStatus() (err error) {
	nodeKey := svc.GetConfigService().GetNodeKey()
	node, err := svc.modelSvc.GetNodeByKey(nodeKey, nil)
	if err != nil {
		return err
	}
	nodeD := delegate.NewModelNodeDelegate(node)
	return nodeD.UpdateStatusOnline()
}

func (svc *MasterService) setWorkerNodeOffline(n interfaces.Node) (err error) {
	return delegate.NewModelNodeDelegate(n).UpdateStatusOffline()
}

func NewMasterService(opts ...Option) (res interfaces.NodeMasterService, err error) {
	// master service
	svc := &MasterService{
		cfgPath:         config.DefaultConfigPath,
		monitorInterval: 60 * time.Second,
		stopOnError:     false,
	}

	// apply options
	for _, opt := range opts {
		opt(svc)
	}

	// dependency options
	var serverOpts []server.Option
	if svc.address != nil {
		serverOpts = append(serverOpts, server.WithAddress(svc.address))
	}

	// dependency injection
	c := dig.New()
	if err := c.Provide(service.NewService); err != nil {
		return nil, err
	}
	if err := c.Provide(config.ProvideConfigService(svc.cfgPath)); err != nil {
		return nil, err
	}
	if err := c.Provide(server.ProvideServer(svc.cfgPath, serverOpts...)); err != nil {
		return nil, err
	}
	if err := c.Invoke(func(cfgSvc interfaces.NodeConfigService, modelSvc service.ModelService, server interfaces.GrpcServer) {
		svc.cfgSvc = cfgSvc
		svc.modelSvc = modelSvc
		svc.server = server
	}); err != nil {
		return nil, err
	}

	// init
	if err := svc.Init(); err != nil {
		return nil, err
	}

	return svc, nil
}

func ProvideMasterService(path string, opts ...Option) func() (interfaces.NodeMasterService, error) {
	if path != "" {
		opts = append(opts, WithConfigPath(path))
	}
	return func() (interfaces.NodeMasterService, error) {
		return NewMasterService(opts...)
	}
}
