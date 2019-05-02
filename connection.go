package graphqlws

import (
	"encoding/json"
	"sync"
	"time"

	"github.com/gorilla/websocket"
	"github.com/lab259/graphql"
	"github.com/lab259/graphql/language/parser"
	"github.com/lab259/rlog"
)

type ConnState string

const (
	connStateUndefined    ConnState = ""
	connStateInitializing ConnState = "initializing"
	connStateEstablished  ConnState = "established"
	connStateClosed       ConnState = "closed"
)

var (
	// operationMessageEOF is a message that when is detected on the writePump, closes the connection.
	operationMessageEOF = &OperationMessage{}
)

type Conn struct {
	Logger            rlog.Logger
	state             ConnState
	handlersMutex     sync.Mutex
	handlers          []Handler
	conn              *websocket.Conn
	config            *Config
	outgoingMessages  chan *OperationMessage
	Schema            *graphql.Schema
	subscriptionMutex sync.Mutex
	Subscriptions     map[string]*Subscription
}

// NewConn initializes a `Conn` instance.
func NewConn(conn *websocket.Conn, schema *graphql.Schema, config *Config) *Conn {
	c := &Conn{
		Schema:           schema,
		config:           config,
		Logger:           rlog.WithFields(nil), // TODO To add the connection ID here.
		conn:             conn,
		outgoingMessages: make(chan *OperationMessage, 10),
		handlers:         make([]Handler, 0, 3),
		Subscriptions:    make(map[string]*Subscription, 3),
	}
	go c.readPump()
	go c.writePump()
	return c
}

// AddHandler adds a `Handler` to the connection.
//
// See also `Handler`
func (c *Conn) AddHandler(handler Handler) {
	c.handlersMutex.Lock()
	defer c.handlersMutex.Unlock()

	c.handlers = append(c.handlers, handler)
}

// RemoveHandler removes a `Handler` from the connection.
//
// See also `Handler`
func (c *Conn) RemoveHandler(handler Handler) {
	c.handlersMutex.Lock()
	defer c.handlersMutex.Unlock()

	hs := c.handlers
	for i, h := range c.handlers {
		if h == handler {
			hs = append(hs[:i], hs[i+1:])
			break
		}
	}
	c.handlers = hs
}

// SendData enqueues a message to be sent by the writePump.
func (c *Conn) SendData(message *OperationMessage) {
	c.outgoingMessages <- message
}

// SendError sends an error to the client.
func (c *Conn) SendError(err error) error {
	if c.state == connStateClosed {
		return ErrConnectionClosed
	}

	errJSON, err2 := json.Marshal(err.Error())
	if err2 != nil {
		return err2
	}
	c.outgoingMessages <- &OperationMessage{
		Type:    gqlTypeError,
		Payload: errJSON,
	}
	return nil
}

func (c *Conn) sendConnectionError(err error) error {
	if c.state == connStateClosed {
		return ErrConnectionClosed
	}

	errJSON, err2 := json.Marshal(err.Error())
	if err2 != nil {
		return err2
	}

	// Write directly to the output channel for being sent to the customer.
	c.outgoingMessages <- &OperationMessage{
		Type:    gqlTypeConnectionError,
		Payload: errJSON,
	}
	return nil
}

func (c *Conn) sendOperationErrors(id string, errs []error) error {
	if c.state == connStateClosed {
		return ErrConnectionClosed
	}

	errJSON, err := json.Marshal(errs)
	if err != nil {
		return err
	}

	// Write directly to the output channel for being sent to the customer.
	c.outgoingMessages <- &OperationMessage{
		Type:    gqlTypeError,
		ID:      id,
		Payload: errJSON,
	}
	return nil
}

func (c *Conn) close() {
	_ = c.conn.Close()
	c.state = connStateClosed
}

func (c *Conn) pongHandler(message string) error {
	// Set the deadline for the next read
	err := c.conn.SetReadDeadline(time.Now().Add(*c.config.PongWait))
	if err != nil {
		return err
	}

	// Go through the handlers and call all `WebsocketPongHandler`s found.
	for _, handler := range c.handlers {
		h, ok := handler.(WebsocketPongHandler)
		if !ok { // If not a `WebsocketPongHandler` try next.
			continue
		}
		err = h.HandleWebsocketPong(message)
		if err != nil {
			return err
		}
	}

	return nil
}

// closeHandler
func (c *Conn) closeHandler(code int, text string) error {
	defaultPrevented := false
	// Go through the handlers and call all `WebsocketCloseHandler`s found.
	for _, handler := range c.handlers {
		h, ok := handler.(WebsocketCloseHandler)
		if !ok { // If not a `ConnectionStartHandler` try next.
			continue
		}
		err := h.HandleWebsocketClose(code, text)
		hErr, ok := err.(*HandlerError)
		if ok {
			if hErr.defaultPrevented {
				defaultPrevented = true
			}
			if hErr.propagationStopped {
				break
			}
		} else if err != nil {
			return err
		}
	}

	if defaultPrevented {
		return nil
	}
	return c.conn.WriteControl(websocket.CloseMessage, websocket.FormatCloseMessage(code, text), time.Now().Add(*c.config.WriteTimeout))
}

func (c *Conn) recover(t RWType) {
	if r := recover(); r != nil {
		defaultPrevented := false

		// Broadcast the message to all handlers attached.
		for _, handler := range c.handlers {
			// Of course, only `SystemRecoverHandler` will be called.
			h, ok := handler.(SystemRecoverHandler)
			if !ok {
				continue
			}
			err := h.HandlePanic(t, r)
			if hErr, ok := err.(*HandlerError); ok {
				if hErr.defaultPrevented {
					defaultPrevented = true
				}
				if hErr.propagationStopped {
					break
				}
			} else if err != nil {
				// TODO
				return
			}
		}

		if defaultPrevented {
			return
		}

		// TODO
	}
}

// addSubscription appends a subscription to the connection.
func (c *Conn) addSubscription(subscription *Subscription) {
	c.subscriptionMutex.Lock()
	defer c.subscriptionMutex.Unlock()

	c.Subscriptions[subscription.ID] = subscription
}

// removeSubscription remove a subcription from the connection.
func (c *Conn) removeSubscription(id string) {
	c.subscriptionMutex.Lock()
	defer c.subscriptionMutex.Unlock()

	delete(c.Subscriptions, id)
}

func (c *Conn) gqlStart(start *GQLStart) {
	errs := make([]error, 0, 1)
	// Go through the handlers and call all `ConnectionStartHandler`s found.
	for _, handler := range c.handlers {
		h, ok := handler.(ConnectionStartHandler)
		if !ok { // If not a `ConnectionStartHandler` try next.
			return
		}
		errsIn := h.HandleConnectionStart(start)
		if len(errs) > 0 { // Keep aggregating errors
			errs = append(errs, errsIn...)
		}
	}

	// If any error has happened ...
	if len(errs) > 0 {
		c.Logger.Error("failed to HandleConnectionStart at gqlStart: ", errs)
		// ... send it to the client.
		err := c.sendOperationErrors(start.ID, errs)
		if err != nil {
			c.Logger.Error("failed to sendOperationErrors when HandleConnectionStart errors at gqlStart: ", err)
		}
		return
	}

	subscription := &Subscription{
		ID:            start.ID,
		Query:         start.Payload.Query,
		Variables:     start.Payload.Variables,
		OperationName: start.Payload.OperationName,
		Connection:    c,
	}

	logger := c.Logger.WithFields(rlog.Fields{
		"subscription": subscription.ID,
	})

	if errors := ValidateSubscription(subscription); len(errors) > 0 {
		logger.WithField("errors", errors).Warn("Failed to add invalid subscription")
		return // errors
	}

	// Parses the subscription query
	document, err := parser.Parse(parser.ParseParams{
		Source: subscription.Query,
	})
	if err != nil {
		logger.WithField("err", err).Warn("Failed to parse subscription query")
		return // []error{err}
	}

	// Validate the query document
	validation := graphql.ValidateDocument(c.Schema, document, nil)
	if !validation.IsValid {
		logger.WithFields(rlog.Fields{
			"errors": validation.Errors,
		}).Error("Failed to validate subscription query")
		return // ErrorsFromGraphQLErrors(validation.Errors)
	}

	// Remember the query document for later
	subscription.Document = document

	// Extract query names from the document (typically, there should only be one)
	subscription.Fields = SubscriptionFieldNamesFromDocument(document)

	c.addSubscription(subscription)

	// Go through the handlers and call all `ConnectionTerminateHandler`s found.
	for _, handler := range c.handlers {
		h, ok := handler.(SubscriptionStartHandler)
		if !ok { // If not a `ConnectionStartHandler` try next.
			continue
		}
		err := h.HandleSubscriptionStart(subscription)
		if hErr, ok := err.(*HandlerError); ok {
			// This event cannot be default prevented.
			if hErr.propagationStopped {
				break
			}
		} else if err != nil {
			c.Logger.Error("error terminating the connection: ", err)
		}
	}
}

func (c *Conn) gqlStop(stop *GQLStop) {
	// Go through the handlers and call all `ConnectionStopHandler`s found.
	for _, handler := range c.handlers {
		h, ok := handler.(ConnectionStopHandler)
		if !ok { // If not a `ConnectionStartHandler` try next.
			continue
		}
		err := h.HandleConnectionStop(stop)
		if err != nil {
			// TODO Call the default error handler.
		}
	}

	subscription, ok := c.Subscriptions[stop.ID]
	if !ok { // If the subscription does not exists.
		c.Logger.Errorf("could not stop a non existing subscription: %s", stop.ID)
		return
	}

	// Go through the handlers and call all `SubscriptionStopHandler`s found.
	for _, handler := range c.handlers {
		h, ok := handler.(SubscriptionStopHandler)
		if !ok { // If not a `ConnectionStartHandler` try next.
			continue
		}
		err := h.HandleSubscriptionStop(subscription)
		if hErr, ok := err.(*HandlerError); ok {
			// This event cannot be default prevented.
			if hErr.propagationStopped {
				break
			}
		} else if err != nil {
			c.Logger.Error("error terminating the connection: ", err)
		}
	}
}

// readPumpIteration runs one read iteration.
func (c *Conn) readPumpIteration() {
	defer c.recover(Read)

	var operationMessage OperationMessage
	err := c.conn.ReadJSON(&operationMessage)
	if err != nil {
		// TODO
		panic(err)
	}

	switch operationMessage.Type {
	case gqlTypeConnectionInit:
		// If the connection is not initializing, it is a protocol error and the
		// connection should be reset.
		if c.state != connStateInitializing {
			panic(ErrReinitializationForbidden)
		}

		var connectionInit GQLConnectionInit
		// Unmarshals the income payload into a `GQLConnectionInit`
		err = json.Unmarshal(operationMessage.Payload, &connectionInit)
		if err != nil {
			// TODO
			panic(err)
		}

		defaultPrevented := false
		// Broadcast the message to all handlers attached.
		for _, handler := range c.handlers {
			// Of course, only `ConnectionInitHandlers` will be called.
			h, ok := handler.(ConnectionInitHandler)
			if !ok {
				continue
			}
			err = h.HandleConnectionInit(&connectionInit)
			if hErr, ok := err.(*HandlerError); ok {
				if hErr.defaultPrevented {
					defaultPrevented = true
				}
				if hErr.propagationStopped {
					break
				}
			} else if err != nil {
				err = c.sendConnectionError(err)
				if err != nil {
					c.Logger.Error("error sending a connection error: ", err)
				}
				return // Returning here have to be checked. It might call the close too early and let the client witout the response.
			}
		}

		if defaultPrevented {
			return
		}

		// Add message to be sent for the writePump
		c.outgoingMessages <- gqlConnectionAck

		// Now the handshake is done.
		c.state = connStateEstablished
	case gqlTypeConnectionTerminate:
		var terminate GQLConnectionTerminate

		// No need to unmarshal a `GQLConnectionTerminate`. The protocol does not define anything.
		// So, why does it exists? Because future improvements might add something there. So it is
		// added to provide further extension witout making it incompatible.

		// Go through the handlers and call all `ConnectionTerminateHandler`s found.
		for _, handler := range c.handlers {
			h, ok := handler.(ConnectionTerminateHandler)
			if !ok { // If not a `ConnectionStartHandler` try next.
				continue
			}
			err := h.HandleConnectionTerminate(&terminate)
			if hErr, ok := err.(*HandlerError); ok {
				// This event cannot be default prevented.
				if hErr.propagationStopped {
					break
				}
			} else if err != nil {
				c.Logger.Error("error terminating the connection: ", err)
			}
		}

		// This should close end readPump and writePump.
		c.close()

		return // Bye bye readPump
	case gqlTypeStart:
		if c.state == connStateEstablished {
			panic(ErrConnectionNotFullyEstablished)
		}
		var start GQLStart
		err = json.Unmarshal(operationMessage.Payload, &start)
		if err != nil {
			c.Logger.Error("failed to unmarshal the payload at gqlStart: ", err)
			err = c.sendOperationErrors(start.ID, []error{err})
			if err != nil {
				c.Logger.Error("failed to sendOperationErrors at gqlStart: ", err)
			}
			return
		}

		c.gqlStart(&start)
	case gqlTypeStop:
		var stop GQLStop
		err = json.Unmarshal(operationMessage.Payload, &stop)
		if err != nil {
			// TODO
			panic(err)
		}

		c.gqlStop(&stop)
	default:
		// TODO To call a default error handler or, maybe, a default message handler.
	}
}

func (c *Conn) readPump() {
	defer c.close()

	c.state = connStateInitializing

	// Prepare for the first pong.
	// The read limit is the size of the package that will be read per once.
	// That, might be adjustable depending your needs.
	c.conn.SetReadLimit(*c.config.ReadLimit)

	c.conn.SetPongHandler(c.pongHandler)
	c.conn.SetCloseHandler(c.closeHandler)

	for c.state != connStateClosed {
		c.conn.SetReadDeadline(time.Now().Add(*c.config.PongWait))
		c.readPumpIteration()
	}
}

func (c *Conn) writePumpIteration() {
	defer c.recover(Write)

	// Ensure the channel is closed before leaving.
	defer func() {
		// Ensure it is safe to close the channel.
		if c.outgoingMessages != nil {
			close(c.outgoingMessages)
			c.outgoingMessages = nil
		}
	}()

	select {
	// Waits until receive a message to be sent.
	case operationMessage, ok := <-c.outgoingMessages:
		if !ok {
			return
		}
		// Well, if this is a EOF, it means that the connection was
		if operationMessage == operationMessageEOF {
			return
		}
		// Schedule a possible write timeout.
		err := c.conn.SetWriteDeadline(time.Now().Add(*c.config.WriteTimeout))
		if err != nil {
			panic(err)
		}
		// Actually writes the response to the websocket connection.
		c.conn.WriteJSON(operationMessage)
	// In case it takes too long to detect a message to be written, we should
	// send a PING to keep the connection open.
	case <-time.After((*c.config.PongWait * 9) / 10):
		err := c.conn.SetWriteDeadline(time.Now().Add(*c.config.WriteTimeout))
		if err != nil {
			panic(err)
		}
		err = c.conn.WriteMessage(websocket.PingMessage, nil)
		if err != nil {
			// TODO
		}
	}
}

func (c *Conn) writePump() {
	defer c.close()

	for c.state != connStateClosed {
		c.writePumpIteration()
	}
}
