package services

import (
	"encoding/json"
	"log"
	"private-chat/core"
	"private-chat/events"
	"time"

	"github.com/gorilla/websocket"
)

// Client is the middleman between the websocket and the hub
type Client struct {
	Hub *Hub
	Conn *websocket.Conn
	Send chan core.EventPayload
	UserId string
	Username string 
}

const (
	// Time allowed to write a message to the peer.
	writeWait = 10 * time.Second
	// Maximum message size allowed from peer 
	maxMessageSize = 1024  
	// Time allowed to read the message from the peer 
	readTimeout = time.Second * 10
	// Send pings to peer with this period. Must be less than pongWait (taking 90% of readTimeout)
	pingPeriod = (readTimeout * 9) / 10
)

func NewClientService() *Client{
	return &Client{}
}

// Pumps messages from the websocket to the hub.
// Hub will only do one thing and that is register the user to the hub
// This application ensures that there is at most one reader per connection 
// running as a goroutine.
func (c *Client) ReadPump() {
	defer func() {
		// unregister client on while terminating the goroutine by sending the client to unregister channel 
		c.Hub.Unregister <- c 
		// close the connection 
		c.Conn.Close()
	}()

	c.Conn.SetReadLimit(maxMessageSize)
	c.Conn.SetReadDeadline(time.Now().Add(readTimeout)) // message will not be read 60 seconds after recieving/processing 
	// send the message to the client (ping) to get a response (pong) and update the deadline if the pong is recieved 
	c.Conn.SetPongHandler(func(string) error { c.Conn.SetReadDeadline(time.Now().Add(readTimeout)); return nil })
	
	// listen for any incoming message from the websocket connection 
	for { // infinite for loop
		var payload core.EventPayload
		if err := c.Conn.ReadJSON(&payload); err != nil {
			if websocket.IsUnexpectedCloseError(err, websocket.CloseGoingAway, websocket.CloseAbnormalClosure) {
				log.Println("Unexpected error : ", err)
				break
			} else {
				log.Println("Error connection broken by client", err)
				break
			}
		}
		// Your interface value holds a map, you can't convert that to a struct. 
		// Use json.RawMessage when unmarshaling, and when you know what type you need, 
		// do a second unmarshaling into that type. You may put this logic into an 
		// UmarshalJSON() function to make it automatic. 
		switch payload.EventName {
		case events.NEW_USER:
			var eventPayload core.NewUserPayload
			// converting map[string]interface{} EventPayload to []bytes so that we convert it later to struct 
			data, _ := json.Marshal(payload.EventPayload)
			json.Unmarshal(data, &eventPayload)
			// data := (payload.EventPayload).(json.RawMessage)
			c.newUserHandler(eventPayload)
			// c.newUserHandler(payload.EventPayload.(core.NewUserPayload))
		case events.DIRECT_MESSAGE:
			c.directMessageHandler(payload.EventPayload.(core.DirectMessagePayload))
		case events.DISCONNECT:
			c.disconnectHandler(payload.EventPayload.(core.DisconnectPayload))
		}
	} // end of for loop 
	
}


// NOT TO BE USED NOW AS THIS LOGIC IS HANDLED BY HUB REGISTER 
func (c *Client) newUserHandler(newUserPayload core.NewUserPayload) {
	// TODO Check if the user is logged in and if not don't do anything (just logged out user tried to create a conn)
	// Register the client 
	// Broadcast the connected users with the new user who has joined with the payload  
	log.Println("The new user has joined w/ username = ", newUserPayload.Username)
	var onlineUsers []core.NewUserPayload = []core.NewUserPayload{}
	for c := range c.Hub.Clients {
		onlineUsers = append(onlineUsers, core.NewUserPayload{Username: c.Username, UserId: c.UserId})
	}
	log.Println("Latest updated list of all the users", onlineUsers)

	// Response sent to all the users
	response := core.EventPayload{
		EventName: events.NEW_USER,
		EventPayload: onlineUsers,
	}

	for client := range c.Hub.Clients {
		select {
		case client.Send <- response:
		default:
			close(client.Send)
			delete(c.Hub.Clients, client)
		}
	}
}

func (c *Client) directMessageHandler(directMessagePayload core.DirectMessagePayload) {
	// Extract out the UserId from payload to which the message needs to be sent 
	receiver := directMessagePayload.Receiver
	// creating response for the receiver 
	response := core.DirectMessageResponse{
		Sender: c.Username,
		Message: directMessagePayload.Message,
		Time: time.Now().String(),
	}
	// Loop over the hub clients and send the message to the specific user 	
	for client := range c.Hub.Clients {
		if client.UserId == receiver {
			client.Send <- core.EventPayload{EventName: events.DIRECT_MESSAGE, EventPayload: response}
			break
		}
	}
	log.Printf("There is a direct message for %v by %v", directMessagePayload.Receiver, directMessagePayload.Receiver)
}

func (c *Client) disconnectHandler(disconnectedUserPayload core.DisconnectPayload) {
	c.Hub.Unregister <- c
	// Broadcast all users with disconnected user list 
	for client := range c.Hub.Clients {
		client.Send <- core.EventPayload{EventName: events.DELETED_USER, EventPayload: disconnectedUserPayload.UserId}	
	}
	log.Printf("The user : %v has disconnected", disconnectedUserPayload.Username)
}

// Pumps message from the hub to the websocket connection  
func (c *Client) WritePump() {
	ticker := time.NewTicker(pingPeriod)
	defer func() {
			ticker.Stop()
			c.Conn.Close()
	}()
	for {
		select {
		case message, ok := <- c.Send:
			// every message will have an eventName attached to it 
			log.Println("writePump: writing to the client", message.EventPayload)
			// Setting a deadline to write this message to the websocket 
			c.Conn.SetWriteDeadline(time.Now().Add(writeWait))
			if !ok {
				// Assuming the hub closed the channel 
				c.Conn.WriteMessage(websocket.CloseMessage, []byte{})
			}

			if err := c.Conn.WriteJSON(message); err == nil {
				log.Println("WritePump(): message write to client successful", message)	
			} else {
				log.Println("WritePump(): ERROR Could not write message to client => ", err)
				return 
			} 
			// if w, err := c.Conn.NextWriter(websocket.TextMessage); err != nil {
			// 	return 
			// } else {
			// 	reqBodyBytes := new(bytes.Buffer)
			// 	if encodeErr := json.NewEncoder(reqBodyBytes).Encode(message); encodeErr != nil {
			// 		return 
			// 	} else {
			// 		log.Println("Message writing to the client", message)
			// 		w.Write(reqBodyBytes.Bytes())
			// 	}
			// }
		case <- ticker.C:
			// Setting a deadline to write this message to the websocket 
			c.Conn.SetWriteDeadline(time.Now().Add(writeWait))
			// Time to send another ping message (for which also, we've put a deadline above)
			if err := c.Conn.WriteMessage(websocket.PingMessage, nil); err != nil {
				return
			}
		}
	}
}	