package graphqlws

// Topic represents a custom interface that represents a topic that will be used
// along with a PubSub system.
type Topic interface {
	// ID will return the structure ID of the topic on the technology used for
	// that purpose. For example, using a Redis PubSub system, this method would
	// return a string containing identifier of the channel.
	ID() interface{}
}

// StringTopic is a simple implementaiton of `Topic` for those PubSub systems
// that use simple strings as topics.
type StringTopic string

func (topic StringTopic) ID() interface{} {
	return topic
}

// SubscriptionSubscriber does subscriptions in behalf a single Subscription
type Subscriber interface {
	// Topics returns the array of topics subscribed.
	// It is designed for accumulating subscriptions before applying it to a
	// connection.
	Topics() []Topic

	// Subscribe does a subcription, or accumulate it (depends on the
	// implementation).
	Subscribe(topic Topic) error
}

type subscriber struct {
	topics []Topic
}

// NewSubscriber creates a default implementation of a subscriber.
func NewSubscriber() *subscriber {
	return &subscriber{
		topics: make([]Topic, 0),
	}
}

func (subscriber *subscriber) Topics() []Topic {
	return subscriber.topics
}

func (subscriber *subscriber) Subscribe(topic Topic) error {
	subscriber.topics = append(subscriber.topics, topic)
	return nil
}
