package bcast

/*
   bcast package for Go. Broadcasting on a set of channels.

   Copyright © 2013 Alexander I.Grafov <grafov@gmail.com>.
   All rights reserved.
   Use of this source code is governed by a BSD-style
   license that can be found in the LICENSE file.
*/

import (
	"container/heap"
	"errors"
	"sync"
	"time"
)

const (
	MSG_TYPE_DATA int = iota
	MSG_TYPE_CLOSE
)

// Message is an internal structure to pack messages together with
// info about sender.
type Message struct {
	msg_type int
	sender   *Member
	payload  interface{}
	clock    int
}

// Member represents member of a Broadcast group.
type Member struct {
	group        *Group
	Read         chan interface{}
	clock        int
	messageQueue PriorityQueue
	send         chan Message
	close        chan bool
}

// Group provides a mechanism for the broadcast of messages to a
// collection of channels.
type Group struct {
	in         chan Message
	close      chan bool
	members    []*Member
	clock      int
	memberLock sync.Mutex
	clockLock  sync.Mutex
}

// NewGroup creates a new broadcast group.
func NewGroup() *Group {
	in := make(chan Message)
	close := make(chan bool)
	return &Group{in: in, close: close, clock: 0}
}

// MemberCount returns the number of members in the Broadcast Group.
func (g *Group) MemberCount() int {
	return len(g.Members())
}

// Members returns a slice of Members that are currently in the Group.
func (g *Group) Members() []*Member {
	g.memberLock.Lock()
	defer g.memberLock.Unlock()
	res := g.members[:]
	return res
}

// Join returns a new member object and handles the creation of its
// output channel.
func (g *Group) Join() *Member {
	memberChannel := make(chan interface{})
	return g.Add(memberChannel)
}

// Leave removes the provided member from the group
func (g *Group) Leave(leaving *Member) error {
	g.memberLock.Lock()
	defer g.memberLock.Unlock()
	memberIndex := -1
	for index, member := range g.members {
		if member == leaving {
			memberIndex = index
			break
		}
	}
	if memberIndex == -1 {
		return errors.New("Could not find provided memeber for removal")
	}
	g.members = append(g.members[:memberIndex], g.members[memberIndex+1:]...)
	go func() {
		leaving.Read <- Message{msg_type: MSG_TYPE_CLOSE, sender: nil, payload: nil}
	}()
	leaving.close <- true // TODO: need to handle the case where there
	// is still stuff in this Members priorityQueue
	return nil
}

// Add adds a member to the group for the provided interface channel.
func (g *Group) Add(memberChannel chan interface{}) *Member {
	g.memberLock.Lock()
	defer g.memberLock.Unlock()

	g.clockLock.Lock()
	defer g.clockLock.Unlock()
	member := &Member{
		group:        g,
		Read:         memberChannel,
		clock:        g.clock,
		messageQueue: PriorityQueue{},
		send:         make(chan Message),
		close:        make(chan bool),
	}
	go member.listen()
	g.members = append(g.members, member)
	return member
}

// Close terminates the group immediately.
func (g *Group) Close() {
	g.close <- true
}

// Broadcast messages received from one group member to others.
// If incoming messages not arrived during `timeout` then function returns.
func (g *Group) Broadcast(timeout time.Duration) {
	var timeoutChannel <-chan time.Time
	if timeout != 0 {
		timeoutChannel = time.After(timeout)
	}
	for {
		select {
		case received := <-g.in:
			g.memberLock.Lock()

			members := g.members[:]

			g.clockLock.Lock()
			received.clock = g.clock
			g.clock++
			g.clockLock.Unlock()

			for _, member := range members {
				// This is done in a goroutine because if it
				// weren't it would be a blocking call
				go func(member *Member, received Message) {
					member.send <- received
				}(member, received)
			}

			g.memberLock.Unlock()
		case <-timeoutChannel:
			if timeout > 0 {
				return
			}
		case <-g.close:
			return
		}
	}
}

// Send broadcasts a message to every one of a Group's members.
func (g *Group) Send(val interface{}) {
	g.in <- Message{msg_type: MSG_TYPE_DATA, sender: nil, payload: val}
}

// Close removes the member it is called on from its broadcast group.
func (m *Member) Close() {
	m.group.Leave(m)
}

// Send broadcasts a message from one Member to the channels of all
// the other members in its group.
func (m *Member) Send(val interface{}) {
	m.group.in <- Message{msg_type: MSG_TYPE_DATA, sender: m, payload: val}
}

// Recv reads one value from the member's Read channel
func (m *Member) Recv() interface{} {
	return <-m.Read
}

func (m *Member) listen() {
	for {
		select {
		case message := <-m.send:
			m.handleMessage(&message)
		case <-m.close:
			return
		}
	}
}

func (m *Member) handleMessage(message *Message) {
	if !m.trySend(message) {
		heap.Push(&m.messageQueue, &Item{
			priority: message.clock,
			value:    message,
		})
		return
	}
	if m.messageQueue.Len() > 0 {
		nextMessage := m.messageQueue[0].value.(*Message)
		for m.trySend(nextMessage) {
			heap.Pop(&m.messageQueue)
			if m.messageQueue.Len() > 0 {
				nextMessage = m.messageQueue[0].value.(*Message)
			} else {
				break
			}
		}
	}
}

func (m *Member) trySend(message *Message) bool {
	shouldSend := message.clock == m.clock
	if shouldSend {
		if message.sender != m {
			if message.msg_type == MSG_TYPE_DATA {
				m.Read <- message.payload
			} else {
				m.Read <- nil
			}
		}
		m.clock++
	}
	return shouldSend
}
