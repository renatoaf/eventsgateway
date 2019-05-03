// eventsgateway
// https://github.com/topfreegames/eventsgateway
//
// Licensed under the MIT license:
// http://www.opensource.org/licenses/mit-license
// Copyright © 2019 Top Free Games <backend@tfgco.com>

package client

import (
	"context"
	"fmt"
	"math"
	"sync"
	"time"

	uuid "github.com/satori/go.uuid"
	"github.com/sirupsen/logrus"
	"github.com/spf13/viper"
	"github.com/topfreegames/eventsgateway/metrics"
	pb "github.com/topfreegames/protos/eventsgateway/grpc/generated"
	"google.golang.org/grpc"
)

type gRPCClientAsync struct {
	client        pb.GRPCForwarderClient
	config        *viper.Viper
	conn          *grpc.ClientConn
	eventsChannel chan *pb.Event
	flushInterval time.Duration
	flushSize     int
	logger        logrus.FieldLogger
	maxRetries    int
	retryInterval time.Duration
	timeout       time.Duration
	wg            sync.WaitGroup
}

func newGRPCClientAsync(
	configPrefix string,
	config *viper.Viper,
	logger logrus.FieldLogger,
	serverAddress string,
	client pb.GRPCForwarderClient,
	opts ...grpc.DialOption,
) (*gRPCClientAsync, error) {
	a := &gRPCClientAsync{
		config: config,
		logger: logger,
	}

	flushIntervalConf := fmt.Sprintf("%sclient.flushInterval", configPrefix)
	a.config.SetDefault(flushIntervalConf, 500*time.Millisecond)
	a.flushInterval = a.config.GetDuration(flushIntervalConf)

	flushSizeConf := fmt.Sprintf("%sclient.flushSize", configPrefix)
	a.config.SetDefault(flushSizeConf, 50)
	a.flushSize = a.config.GetInt(flushSizeConf)

	channelBufferConf := fmt.Sprintf("%sclient.channelBuffer", configPrefix)
	a.config.SetDefault(channelBufferConf, 200)
	channelBuffer := a.config.GetInt(channelBufferConf)
	a.eventsChannel = make(chan *pb.Event, channelBuffer)

	maxRetriesConf := fmt.Sprintf("%sclient.maxRetries", configPrefix)
	a.config.SetDefault(maxRetriesConf, 3)
	a.maxRetries = a.config.GetInt(maxRetriesConf)

	retryIntervalConf := fmt.Sprintf("%sclient.retryInterval", configPrefix)
	a.config.SetDefault(retryIntervalConf, 1*time.Second)
	a.retryInterval = a.config.GetDuration(retryIntervalConf)

	timeoutConf := fmt.Sprintf("%sclient.grpc.timeout", configPrefix)
	a.config.SetDefault(timeoutConf, 500*time.Millisecond)
	a.timeout = a.config.GetDuration(timeoutConf)

	a.logger = a.logger.WithFields(logrus.Fields{
		"flushInterval": a.flushInterval,
		"flushSize":     a.flushSize,
		"channelBuffer": channelBuffer,
		"timeout":       a.timeout,
	})

	if err := a.configureGRPCForwarderClient(serverAddress, client); err != nil {
		return nil, err
	}

	numRoutinesConf := fmt.Sprintf("%sclient.numRoutines", configPrefix)
	a.config.SetDefault(numRoutinesConf, 5)
	numSendRoutines := a.config.GetInt(numRoutinesConf)

	a.logger = a.logger.WithFields(logrus.Fields{
		"numRoutines": numSendRoutines,
	})

	for i := 0; i < numSendRoutines; i++ {
		go a.sendRoutine()
	}

	return a, nil
}

func (a *gRPCClientAsync) configureGRPCForwarderClient(
	serverAddress string,
	client pb.GRPCForwarderClient,
	opts ...grpc.DialOption,
) error {
	if client != nil {
		a.client = client
		return nil
	}
	a.logger.WithFields(logrus.Fields{
		"operation": "configureGRPCForwarderClient",
	}).Info("connecting to grpc server")
	dialOpts := append(
		[]grpc.DialOption{
			grpc.WithInsecure(),
			grpc.WithUnaryInterceptor(a.metricsReporterInterceptor),
		},
		opts...,
	)
	conn, err := grpc.Dial(serverAddress, dialOpts...)
	if err != nil {
		return err
	}
	a.conn = conn
	a.client = pb.NewGRPCForwarderClient(conn)
	return nil
}

// metricsReporterInterceptor will report metrics from client
func (a *gRPCClientAsync) metricsReporterInterceptor(
	ctx context.Context,
	method string,
	req interface{},
	reply interface{},
	cc *grpc.ClientConn,
	invoker grpc.UnaryInvoker,
	opts ...grpc.CallOption,
) error {
	l := a.logger.WithFields(logrus.Fields{
		"method": method,
	})

	events := req.(*pb.SendEventsRequest).Events
	retry := fmt.Sprintf("%d", req.(*pb.SendEventsRequest).Retry)

	defer func(startTime time.Time) {
		elapsedTime := float64(time.Since(startTime).Nanoseconds() / 1000000)
		for _, e := range events {
			metrics.ClientRequestsResponseTime.WithLabelValues(
				hostname,
				method,
				e.Topic,
				retry,
			).Observe(elapsedTime)
		}
		l.WithFields(logrus.Fields{
			"elapsedTime": elapsedTime,
			"reply":       reply.(*pb.SendEventsResponse),
		}).Debug("request processed")
	}(time.Now())

	if err := invoker(ctx, method, req, reply, cc, opts...); err != nil {
		l.WithError(err).Error("error processing request")
		for _, e := range events {
			metrics.ClientRequestsFailureCounter.WithLabelValues(
				hostname,
				method,
				e.Topic,
				retry,
				err.Error(),
			).Inc()
		}
	}
	for _, e := range events {
		metrics.ClientRequestsSuccessCounter.WithLabelValues(
			hostname,
			method,
			e.Topic,
			retry,
		).Inc()
	}
	return nil
}

func (a *gRPCClientAsync) send(ctx context.Context, event *pb.Event) error {
	a.wg.Add(1)
	a.eventsChannel <- event
	return nil
}

func (a *gRPCClientAsync) sendRoutine() {
	ticker := time.NewTicker(a.flushInterval)
	defer ticker.Stop()

	req := &pb.SendEventsRequest{}
	req.Events = make([]*pb.Event, 0, a.flushSize)

	send := func() {
		cpy := req
		cpy.Id = uuid.NewV4().String()
		req = &pb.SendEventsRequest{}
		req.Events = make([]*pb.Event, 0, a.flushSize)
		go a.sendEvents(cpy, 0)
	}

	for {
		select {
		case e := <-a.eventsChannel:
			if len(req.Events) == 0 {
				a.wg.Add(1)
			}
			a.wg.Done()
			req.Events = append(req.Events, e)
			if len(req.Events) == a.flushSize {
				send()
			}
		case <-ticker.C:
			if len(req.Events) > 0 {
				send()
			}
		}
	}
}

func (a *gRPCClientAsync) sendEvents(req *pb.SendEventsRequest, retryCount int) {
	l := a.logger.WithFields(logrus.Fields{
		"operation":  "sendEvents",
		"requestId":  req.Id,
		"retryCount": retryCount,
		"size":       len(req.Events),
	})
	if retryCount > a.maxRetries {
		l.Info("dropped events due to max retries")
		a.wg.Done()
		return
	}
	ctx, cancel := context.WithTimeout(context.Background(), a.timeout)
	defer cancel()
	// in case server's producer fail to send any event, failure indexes are sent
	// in response to be retried
	res, err := a.client.SendEvents(ctx, req)
	if err != nil {
		l.WithError(err).Error("failed to send events")
		time.Sleep(time.Duration(math.Pow(2, float64(retryCount))) * a.retryInterval)
		a.sendEvents(req, retryCount+1)
		return
	}
	if res.FailureIndexes != nil && len(res.FailureIndexes) != 0 {
		l.WithFields(logrus.Fields{
			"failureIndexes": res.FailureIndexes,
		}).Error("failed to send events")
		time.Sleep(time.Duration(math.Pow(2, float64(retryCount))) * a.retryInterval)
		events := make([]*pb.Event, 0, len(res.FailureIndexes))
		for _, index := range res.FailureIndexes {
			events = append(events, req.Events[index])
		}
		req.Events = events
		a.sendEvents(req, retryCount+1)
		return
	}
	a.wg.Done()
}

// GracefulStop waits pending async send of events and closes client connection
func (a *gRPCClientAsync) GracefulStop() error {
	a.wg.Wait()
	return a.conn.Close()
}
