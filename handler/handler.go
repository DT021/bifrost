package handler

import (
	"github.com/osstotalsoft/bifrost/abstraction"
	"net/http"
)

//ReverseProxyHandlerType is a handler type, used when registering a reverse proxy handler
const ReverseProxyHandlerType = "reverseproxy"

//EventPublisherHandlerType is a handler type, used when registering an event handler
//ex: nats, kafka
const EventPublisherHandlerType = "event"

//Func is a signature that each handler must implement
type Func func(endpoint abstraction.Endpoint) http.Handler
