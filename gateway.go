package main

import (
	"github.com/osstotalsoft/bifrost/config"
	"github.com/osstotalsoft/bifrost/handlers"
	"github.com/osstotalsoft/bifrost/servicediscovery"
	"github.com/osstotalsoft/bifrost/utils"
	"net/http"
	"strconv"
	"sync"

	log "github.com/sirupsen/logrus"
)

type PreFilterFunc func(request *http.Request) error
type PostFilterFunc func(request, proxyRequest *http.Request, proxyResponse *http.Response) ([]byte, error)
type MiddlewareFunc func(http.Handler) http.Handler

type Gateway struct {
	preFilters            []PreFilterFunc
	config                *config.Config
	endPointToRouteMapper sync.Map
	middlewares           map[string]MiddlewareFunc
}

type Endpoint struct {
	UpstreamPath         string
	UpstreamPathPrefix   string
	UpstreamURL          string
	DownstreamPath       string
	DownstreamPathPrefix string
	Methods              []string
}

func NewGateway(config *config.Config) *Gateway {
	if config == nil {
		log.Panicf("Gateway: Must provide a configuration file")
	}
	return &Gateway{
		config:     config,
		preFilters: []PreFilterFunc{},
	}
}

type AddServiceFunc func(addRouteFunc AddRouteFunc) func(service servicediscovery.Service)
type AddRouteFunc func(path string, pathPrefix string, methods []string, handler http.Handler) (string, error)
type UpdateEndpointFunc func(addRouteFunc AddRouteFunc, removeRouteFunc func(routeId string)) func(oldService servicediscovery.Service, newService servicediscovery.Service)

func AddService(gate *Gateway) AddServiceFunc {
	return func(addRouteFunc AddRouteFunc) func(service servicediscovery.Service) {
		return func(service servicediscovery.Service) {
			internalAddService(gate, service, addRouteFunc)
		}
	}
}

func AddPreFilter(gate *Gateway) func(f PreFilterFunc) {
	return func(f PreFilterFunc) {
		gate.preFilters = append(gate.preFilters, f)
	}
}

func UpdateService(gate *Gateway) UpdateEndpointFunc {
	return func(addRouteFunc AddRouteFunc, removeRouteFunc func(routeId string)) func(oldService servicediscovery.Service, newService servicediscovery.Service) {
		return func(oldService servicediscovery.Service, newService servicediscovery.Service) {
			//removing routes
			removeRoutes(gate, oldService, removeRouteFunc)
			//adding routes
			internalAddService(gate, newService, addRouteFunc)
		}
	}
}

func UseMiddleware(gate *Gateway) func(key string, mwf MiddlewareFunc) {
	return func(key string, mwf MiddlewareFunc) {
		gate.middlewares[key] = mwf
	}
}

func RemoveService(gate *Gateway) func(removeRouteFunc func(routeId string)) func(service servicediscovery.Service) {
	return func(removeRouteFunc func(routeId string)) func(service servicediscovery.Service) {
		return func(service servicediscovery.Service) {
			removeRoutes(gate, service, removeRouteFunc)
		}
	}
}

func internalAddService(gate *Gateway, service servicediscovery.Service, addRouteFunc AddRouteFunc) []Endpoint {
	var routes []string

	endpoints := createEndpoints(gate.config, service)
	for _, endp := range endpoints {
		routeId, _ := addRouteFunc(endp.DownstreamPath, endp.DownstreamPathPrefix, endp.Methods, getEndpointHandlers(gate, endp))
		routes = append(routes, routeId)
	}
	gate.endPointToRouteMapper.Store(service.UID, routes)

	return endpoints
}

func removeRoutes(gate *Gateway, oldService servicediscovery.Service, removeRouteFunc func(routeId string)) {
	gate.endPointToRouteMapper.Range(func(key, value interface{}) bool {
		if key == oldService.UID {
			for _, rId := range value.([]string) {
				removeRouteFunc(rId)
			}
			return false
		}
		return true
	})
}

func createEndpoints(config *config.Config, service servicediscovery.Service) []Endpoint {
	configEndpoints := findConfigEndpoints(config.Endpoints, service.Resource)
	var endPoints []Endpoint

	for _, endp := range configEndpoints {
		var endPoint Endpoint

		endPoint.DownstreamPathPrefix = endp.DownstreamPathPrefix
		if endPoint.DownstreamPathPrefix == "" {
			endPoint.DownstreamPathPrefix = utils.SingleJoiningSlash(config.DownstreamPathPrefix, service.Resource)
		}
		endPoint.UpstreamPathPrefix = endp.UpstreamPathPrefix
		if endPoint.UpstreamPathPrefix == "" {
			endPoint.UpstreamPathPrefix = config.UpstreamPathPrefix
		}

		endPoint.UpstreamURL = utils.SingleJoiningSlash(service.Address, utils.SingleJoiningSlash(endPoint.UpstreamPathPrefix, endp.UpstreamPath))
		endPoint.UpstreamPath = endp.UpstreamPath
		endPoint.DownstreamPath = endp.DownstreamPath
		endPoint.Methods = endp.Methods
		endPoints = append(endPoints, endPoint)
	}

	//add default route if no config found
	if len(endPoints) == 0 {
		var endPoint Endpoint

		endPoint.DownstreamPathPrefix = utils.SingleJoiningSlash(config.DownstreamPathPrefix, service.Resource)
		endPoint.UpstreamURL = utils.SingleJoiningSlash(service.Address, config.UpstreamPathPrefix)
		endPoint.UpstreamPathPrefix = config.UpstreamPathPrefix
		log.Infof("Gateway: Applied default configuration for service %v", service)
		endPoints = append(endPoints, endPoint)
	}

	return endPoints
}

func findConfigEndpoints(endpoints []config.Endpoint, serviceName string) []config.Endpoint {
	var result []config.Endpoint //endpoints[:0]
	for _, endp := range endpoints {
		if endp.ServiceName == serviceName {
			result = append(result, endp)
		}
	}
	return result
}

func handleFilterError(responseWriter http.ResponseWriter, request *http.Request, err error) {
	responseWriter.Header().Set("Content/Type", "text/html")
	responseWriter.WriteHeader(500)
	_, err = responseWriter.Write([]byte(err.Error()))
	if err != nil {
		log.Errorln(err)
	}
}

func GatewayListenAndServe(gate *Gateway, handler http.Handler) error {
	return http.ListenAndServe(":"+strconv.Itoa(gate.config.Port), handler)
}

func getEndpointHandlers(gate *Gateway, endPoint Endpoint) http.Handler {

	//for i := len(gate.middlewares) - 1; i >= 0; i-- {
	//	match.Handler = gate.middlewares[i](match.Handler)
	//}
	revProxy := handlers.NewReverseProxy(endPoint.UpstreamURL, endPoint.UpstreamPath, endPoint.UpstreamPathPrefix)

	return http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		writer.Header().Set("X-Gateway", "GoGateway")
		err := runPreFilters(gate.preFilters, request)
		if err != nil {
			handleFilterError(writer, request, err)
			return
		}

		revProxy.ServeHTTP(writer, request)
	})
}

func runPreFilters(preFilters []PreFilterFunc, request *http.Request) error {
	for _, filter := range preFilters {
		err := filter(request)
		if err != nil {
			return err
		}
	}
	return nil
}
