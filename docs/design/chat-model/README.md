# The chat delivery model

A first TLA+ sketch of one delivery of the agent chat, the formal model that
[the chat design](../chat.md#150-before-code-the-formal-model) requires before code (W-091).

- [`ChatDelivery.tla`](ChatDelivery.tla) models one message to one recipient: the native route
  and its move to the hooks route, a pusher that can crash at any step, the recipient's reads, acks
  and end, the sweep, and a deadline that passes at any moment.
- [`ChatDelivery.cfg`](ChatDelivery.cfg) sets `MaxAttempts` to 4 and lists the properties to check:
  `TypeOK`, `AtMostOnce`, `NoEarlyExposure`, `NeverLooksUnpushed` and `FinalStable`.

The sketch has not been run through TLC yet. Checking it, and widening it to the bridge route,
requests and responses, owed notices and the journal of section 7.3, is the first deliverable of the
implementation. With the TLA+ tools installed, it is checked with:

```sh
java -cp tla2tools.jar tlc2.TLC -config ChatDelivery.cfg ChatDelivery.tla
```
