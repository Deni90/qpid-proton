#
# Licensed to the Apache Software Foundation (ASF) under one
# or more contributor license agreements.  See the NOTICE file
# distributed with this work for additional information
# regarding copyright ownership.  The ASF licenses this file
# to you under the Apache License, Version 2.0 (the
# "License"); you may not use this file except in compliance
# with the License.  You may obtain a copy of the License at
#
#   http://www.apache.org/licenses/LICENSE-2.0
#
# Unless required by applicable law or agreed to in writing,
# software distributed under the License is distributed on an
# "AS IS" BASIS, WITHOUT WARRANTIES OR CONDITIONS OF ANY
# KIND, either express or implied.  See the License for the
# specific language governing permissions and limitations
# under the License.
#
import collections, Queue, socket, time, threading
from proton import ConnectionException, Delivery, Endpoint, Handler, LinkException, Message, Timeout, Url
from proton.reactors import AmqpSocket, Container, Events, SelectLoop, send_msg
from proton.handlers import Acking, MessagingHandler, ScopedHandler, IncomingMessageHandler

class BlockingLink(object):
    def __init__(self, connection, link):
        self.connection = connection
        self.link = link
        self.connection.wait(lambda: not (self.link.state & Endpoint.REMOTE_UNINIT),
                             msg="Opening link %s" % link.name)
        if self.link.state & Endpoint.REMOTE_CLOSED:
            self.link.close()
            raise LinkException("Failed to open link %s" % link.name)

    def close(self):
        self.link.close()
        self.connection.wait(lambda: not (self.link.state & Endpoint.REMOTE_ACTIVE),
                             msg="Closing link %s" % self.link.name)

    # Access to other link attributes.
    def __getattr__(self, name): return getattr(self.link, name)

class BlockingSender(BlockingLink):
    def __init__(self, connection, sender):
        super(BlockingSender, self).__init__(connection, sender)
        if self.link.target and self.link.target.address and self.link.target.address != self.link.remote_target.address:
            self.link.close()
            raise LinkException("Failed to open sender %s, target does not match" % self.link.name)

    def send_msg(self, msg, timeout=False):
        delivery = send_msg(self.link, msg)
        self.connection.wait(lambda: delivery.settled, msg="Sending on sender %s" % self.link.name, timeout=timeout)

class Fetcher(MessagingHandler):
    def __init__(self, prefetch):
        super(Fetcher, self).__init__(prefetch=prefetch, auto_accept=False)
        self.incoming = collections.deque([])
        self.unsettled = collections.deque([])

    def on_message(self, event):
        self.incoming.append((event.message, event.delivery))

    def on_link_error(self, event):
        # This will be handled by BlockingConnection
        pass

    @property
    def has_message(self):
        return len(self.incoming)

    def pop(self):
        message, delivery = self.incoming.popleft()
        if not delivery.settled:
            self.unsettled.append(delivery)
        return message

    def settle(self, state=None):
        delivery = self.unsettled.popleft()
        if state:
            delivery.update(state)
        delivery.settle()


class BlockingReceiver(BlockingLink):
    def __init__(self, connection, receiver, fetcher, credit=1):
        super(BlockingReceiver, self).__init__(connection, receiver)
        if self.link.source and self.link.source.address and self.link.source.address != self.link.remote_source.address:
            self.link.close()
            raise LinkException("Failed to open receiver %s, source does not match" % self.link.name)
        if credit: receiver.flow(credit)
        self.fetcher = fetcher

    def fetch(self, timeout=False):
        if not self.fetcher:
            raise Exception("Can't call fetch on this receiver as a handler was provided")
        if not self.link.credit:
            self.link.flow(1)
        self.connection.wait(lambda: self.fetcher.has_message, msg="Fetching on receiver %s" % self.link.name, timeout=timeout)
        return self.fetcher.pop()

    def accept(self):
        self.settle(Delivery.ACCEPTED)

    def reject(self):
        self.settle(Delivery.REJECTED)

    def release(self, delivered=True):
        if delivered:
            self.settle(Delivery.MODIFIED)
        else:
            self.settle(Delivery.RELEASED)

    def settle(self, state=None):
        if not self.fetcher:
            raise Exception("Can't call accept/reject etc on this receiver as a handler was provided")
        self.fetcher.settle(state)


class BlockingConnection(Handler):
    """
    A synchronous style connection wrapper.
    """
    def __init__(self, url, timeout=None, container=None):
        self.timeout = timeout
        self.container = container or Container()
        self.url = Url(url).defaults()
        self.conn = self.container.connect(url=self.url, handler=self)
        self.wait(lambda: not (self.conn.state & Endpoint.REMOTE_UNINIT),
                  msg="Opening connection")

    def create_sender(self, address, handler=None, name=None, options=None):
        return BlockingSender(self, self.container.create_sender(self.conn, address, name=name, handler=handler))

    def create_receiver(self, address, credit=None, dynamic=False, handler=None, name=None, options=None):
        prefetch = credit
        if handler:
            fetcher = None
            if prefetch is None:
                prefetch = 1
        else:
            fetcher = Fetcher(credit)
        return BlockingReceiver(
            self, self.container.create_receiver(self.conn, address, name=name, dynamic=dynamic, handler=handler or fetcher), fetcher, credit=prefetch)

    def close(self):
        self.conn.close()
        self.wait(lambda: not (self.conn.state & Endpoint.REMOTE_ACTIVE),
                  msg="Closing connection")

    def run(self):
        """ Hand control over to the event loop (e.g. if waiting indefinitely for incoming messages) """
        self.container.run()

    def wait(self, condition, timeout=False, msg=None):
        """Call do_work until condition() is true"""
        if timeout is False:
            timeout = self.timeout
        if timeout is None:
            while not condition():
                self.container.do_work()
        else:
            deadline = time.time() + timeout
            while not condition():
                if not self.container.do_work(deadline - time.time()):
                    txt = "Connection %s timed out" % self.url
                    if msg: txt += ": " + msg
                    raise Timeout(txt)

    def on_link_remote_close(self, event):
        if event.link.state & Endpoint.LOCAL_ACTIVE:
            event.link.close()
            if event.link.is_sender:
                txt = "sender %s to %s closed" % (event.link.name, event.link.target.address)
            else:
                txt = "receiver %s from %s closed" % (event.link.name, event.link.source.address)
            if event.link.remote_condition:
                txt += " due to: %s" % event.link.remote_condition
            else:
                txt += " by peer"
            raise LinkException(txt)

    def on_connection_remote_close(self, event):
        if event.connection.state & Endpoint.LOCAL_ACTIVE:
            event.connection.close()
            txt = "Connection %s closed" % self.url
            if event.connection.remote_condition:
                txt += " due to: %s" % event.connection.remote_condition
            else:
                txt += " by peer"
            raise ConnectionException(txt)

    def on_disconnected(self, event):
        raise ConnectionException("Connection %s disconnected" % self.url);

class AtomicCount(object):
    def __init__(self, start=0, step=1):
        """Thread-safe atomic counter. Start at start, increment by step."""
        self.count, self.step = start, step
        self.lock = threading.Lock()

    def next(self):
        """Get the next value"""
        self.lock.acquire()
        self.count += self.step;
        result = self.count
        self.lock.release()
        return result

class SyncRequestResponse(IncomingMessageHandler):
    """
    Implementation of the synchronous request-responce (aka RPC) pattern.
    """

    correlation_id = AtomicCount()

    def __init__(self, connection, address=None):
        """
        Send requests and receive responses. A single instance can send many requests
        to the same or different addresses.

        @param connection: A L{BlockingConnection}
        @param address: Address for all requests.
            If not specified, each request must have the address property set.
            Sucessive messages may have different addresses.

        @ivar address: Address for all requests, may be None.
        @ivar connection: Connection for requests and responses.
        """
        super(SyncRequestResponse, self).__init__()
        self.connection = connection
        self.address = address
        self.sender = self.connection.create_sender(self.address)
        # dynamic=true generates a unique address dynamically for this receiver.
        # credit=1 because we want to receive 1 response message initially.
        self.receiver = self.connection.create_receiver(None, dynamic=True, credit=1, handler=self)
        self.response = None

    def call(self, request):
        """
        Send a request message, wait for and return the response message.

        @param request: A L{proton.Message}. If L{self.address} is not set the 
            L{request.address} must be set and will be used.
        """
        if not self.address and not request.address:
            raise ValueError("Request message has no address: %s" % request)
        request.reply_to = self.reply_to
        request.correlation_id = correlation_id = self.correlation_id.next()
        self.sender.send_msg(request)
        def wakeup():
            return self.response and (self.response.correlation_id == correlation_id)
        self.connection.wait(wakeup, msg="Waiting for response")
        response = self.response
        self.response = None    # Ready for next response.
        self.receiver.flow(1)   # Set up credit for the next response.
        return response

    @property
    def reply_to(self):
        """Return the dynamic address of our receiver."""
        return self.receiver.remote_source.address

    def on_message(self, event):
        """Called when we receive a message for our receiver."""
        self.response = event.message