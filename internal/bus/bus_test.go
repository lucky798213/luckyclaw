package bus

import "testing"

func TestConversationAddressAccount(t *testing.T) {
	address := ConversationAddress{
		Channel:   "telegram",
		AccountID: "bot-1",
		ChatID:    "chat-1",
		ThreadID:  "topic-1",
	}
	want := ChannelAccount{Channel: "telegram", AccountID: "bot-1"}
	if got := address.Account(); got != want {
		t.Fatalf("Account() = %+v, want %+v", got, want)
	}
}

func TestMessagesExposeConversationAddress(t *testing.T) {
	want := ConversationAddress{
		Channel:   "feishu",
		AccountID: "app-1",
		ChatID:    "chat-1",
		ThreadID:  "thread-1",
	}
	inbound := InboundMessage{
		Channel:   want.Channel,
		AccountID: want.AccountID,
		ChatID:    want.ChatID,
		ThreadID:  want.ThreadID,
	}
	outbound := OutboundMessage{
		Channel:   want.Channel,
		AccountID: want.AccountID,
		ChatID:    want.ChatID,
		ThreadID:  want.ThreadID,
	}
	if got := inbound.Address(); got != want {
		t.Fatalf("InboundMessage.Address() = %+v, want %+v", got, want)
	}
	if got := outbound.Address(); got != want {
		t.Fatalf("OutboundMessage.Address() = %+v, want %+v", got, want)
	}
}
