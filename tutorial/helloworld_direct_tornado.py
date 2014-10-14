#!/usr/bin/env python
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

from proton import Message
from proton_events import ClientHandler, FlowController, Handshaker, IncomingMessageHandler
from proton_tornado import TornadoLoop

class HelloWorldReceiver(IncomingMessageHandler):
    def on_message(self, event):
        print event.message.body
        event.connection.close()

class HelloWorld(ClientHandler):
    def __init__(self, eventloop, url, address):
        self.eventloop = eventloop
        self.acceptor = eventloop.listen(url)
        self.conn = eventloop.connect(url, handler=self)
        self.address = address

    def on_connection_opened(self, event):
        self.conn.create_sender(self.address)

    def on_credit(self, event):
        event.sender.send_msg(Message(body=u"Hello World!"))
        event.sender.close()

    def on_accepted(self, event):
        self.conn.close()

    def on_connection_closed(self, event):
        self.acceptor.close()
        self.eventloop.stop()

    def run(self):
        self.eventloop.run()

eventloop = TornadoLoop(HelloWorldReceiver(), Handshaker(), FlowController(1))
HelloWorld(eventloop, "localhost:8888", "examples").run()

