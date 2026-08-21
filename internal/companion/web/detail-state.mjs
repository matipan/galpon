function mirroredResponseIDs(detail) {
  return new Set((detail?.mirroredDeliveryResponses || []).map((id) => `delivery:${id}:response`));
}

function combinedMirroredDeliveries(first, second) {
  return [...new Set([
    ...(first?.mirroredDeliveryResponses || []),
    ...(second?.mirroredDeliveryResponses || []),
  ])];
}

function stableTimelineOrder(events, local = []) {
  const values = Array.isArray(events) ? events : [];
  const durable = values.filter((event) => Number(event?.seq || 0) > 0);
  // A refreshed tail can include an older anchored prompt. The merge adds the
  // retained prefix before that fresh tail, so restore the authoritative Pi
  // sequence here. Otherwise the prompt can land inside its own tool phase and
  // split one action group into changing counts on every refresh.
  durable.sort((first, second) => Number(first.seq) - Number(second.seq));
  return [
    ...durable,
    ...values.filter((event) => Number(event?.seq || 0) <= 0),
    ...local,
  ];
}

function realEventIDs(detail) {
  return (detail?.timeline || [])
    .filter((event) => Number(event?.seq || 0) > 0 && event?.isAnchor !== true)
    .map((event) => String(event?.eventId || ""));
}

export function mergeRefreshedDetail(previous, fresh) {
  if (!previous || previous.agent.id !== fresh.agent.id) return fresh;
  const mirroredResponses = mirroredResponseIDs(fresh);
  const mirroredDeliveryResponses = combinedMirroredDeliveries(previous, fresh);
  const freshIDs = new Set(fresh.timeline.map((event) => String(event?.eventId || "")));
  const previousRealIDs = realEventIDs(previous);
  const freshRealIDs = new Set(realEventIDs(fresh));
  const previousMessagePageIDs = previous.messagePageIds || [];
  const freshMessagePageIDs = new Set(fresh.messagePageIds || []);
  const preserveRealRange = previousRealIDs.length > 0 && freshRealIDs.size > 0
    && previousRealIDs.some((id) => freshRealIDs.has(id));
  const preserveMessageRange = previousMessagePageIDs.length > 0 && freshMessagePageIDs.size > 0
    && previousMessagePageIDs.some((id) => freshMessagePageIDs.has(id));

  const local = previous.timeline.filter((event) => {
    const id = String(event?.eventId || "");
    return event?.localOnly === true && !freshIDs.has(id);
  });
  const older = previous.timeline.filter((event) => {
    const id = String(event?.eventId || "");
    const sequence = Number(event?.seq || 0);
    if (event?.localOnly === true || freshIDs.has(id) || (sequence <= 0 && mirroredResponses.has(id))) return false;
    if (sequence > 0) return preserveRealRange && fresh.before > 0 && sequence < fresh.before;
    return preserveMessageRange;
  });
  return {
    ...fresh,
    timeline: stableTimelineOrder([...older, ...fresh.timeline], local),
    conversationHasMore: preserveRealRange ? previous.conversationHasMore : fresh.conversationHasMore,
    before: preserveRealRange ? previous.before : fresh.before,
    messageHasMore: preserveMessageRange ? previous.messageHasMore : fresh.messageHasMore,
    messageBefore: preserveMessageRange ? previous.messageBefore : fresh.messageBefore,
    hasMore: (preserveRealRange ? previous.conversationHasMore : fresh.conversationHasMore)
      || (preserveMessageRange ? previous.messageHasMore : fresh.messageHasMore),
    mirroredDeliveryResponses,
  };
}

export function mergeIncrementalDetail(previous, fresh) {
  if (!previous || previous.agent.id !== fresh.agent.id) return fresh;
  const mirroredResponses = mirroredResponseIDs(fresh);
  const mirroredDeliveryResponses = combinedMirroredDeliveries(previous, fresh);
  const freshIDs = new Set(fresh.timeline.map((event) => String(event?.eventId || "")));
  const local = previous.timeline.filter((event) =>
    event?.localOnly === true && !freshIDs.has(String(event?.eventId || "")));
  const retained = previous.timeline.filter((event) => {
    const id = String(event?.eventId || "");
    const syntheticMirroredResponse = Number(event?.seq || 0) <= 0 && mirroredResponses.has(id);
    return event?.localOnly !== true && !freshIDs.has(id) && !syntheticMirroredResponse;
  });
  const conversationHasMore = previous.conversationHasMore;
  const messageHasMore = previous.messageHasMore || fresh.messageHasMore;
  return {
    ...fresh,
    timeline: stableTimelineOrder([...retained, ...fresh.timeline], local),
    conversationHasMore,
    before: previous.before,
    messageHasMore,
    messageBefore: previous.messageBefore || fresh.messageBefore,
    hasMore: conversationHasMore || messageHasMore,
    mirroredDeliveryResponses,
  };
}

export function mergeOlderDetail(current, older) {
  const mirroredResponses = mirroredResponseIDs(older);
  const base = current.timeline.filter((event) =>
    Number(event?.seq || 0) > 0 || !mirroredResponses.has(String(event?.eventId || "")));
  const seen = new Set(base.map((event) => String(event?.eventId || "")));
  const conversationRequested = current.conversationHasMore && current.before > 0;
  const messageRequested = current.messageHasMore && Boolean(current.messageBefore);
  const conversationHasMore = conversationRequested ? older.conversationHasMore : current.conversationHasMore;
  const messageHasMore = messageRequested ? older.messageHasMore : current.messageHasMore;
  return {
    ...current,
    timeline: stableTimelineOrder([...older.timeline.filter((event) => !seen.has(String(event?.eventId || ""))), ...base]),
    conversationHasMore,
    before: conversationRequested ? older.before : current.before,
    messageHasMore,
    messageBefore: messageRequested ? older.messageBefore : current.messageBefore,
    hasMore: conversationHasMore || messageHasMore,
    mirroredDeliveryResponses: combinedMirroredDeliveries(current, older),
  };
}
