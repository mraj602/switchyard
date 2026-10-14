// A server-sent events parser (WHATWG EventSource rules), pure and incremental: feed it text
// chunks as they arrive, split anywhere (even between a CR and its LF, or in the middle of a
// field name), and it calls back per event. No Node and no Electron, so tests run anywhere.
//
//   const p = createParser({onEvent, onComment, onRetry});
//   p.feed('id: 7\nevent: ses'); p.feed('sion\ndata: {"a":1}\n\n');
//
// Rules: lines end with LF, CRLF or CR. A line starting with ':' is a comment. `data` lines
// are joined with LF. An empty line dispatches the event, unless there was no `data` (an event
// made of only an `id` or `event` field is not dispatched, but the `id` still counts as the last
// event id). `retry` takes digits only. An `id` with a NUL is ignored. One leading BOM is dropped.

/**
 * @param {{onEvent?: (e: {event: string, data: string, id: string}) => void,
 *          onComment?: (text: string) => void,
 *          onRetry?: (ms: number) => void,
 *          maxLine?: number}} [handlers]
 *   maxLine: a line longer than this (default 1 MiB) is dropped, so a hostile or broken server
 *   cannot grow memory without bound.
 */
export function createParser(handlers = {}) {
  const maxLine = handlers.maxLine ?? 1024 * 1024;
  let buffer = ''; // the incomplete tail line
  let sawCR = false; // the last chunk ended with CR: a following LF belongs to it
  let first = true;
  let dropping = false; // inside an over-long line
  let lastId = '';
  let event = '';
  let data = [];
  let hasData = false;

  const dispatch = () => {
    if (hasData) handlers.onEvent?.({ event: event || 'message', data: data.join('\n'), id: lastId });
    event = '';
    data = [];
    hasData = false;
  };

  const line = (l) => {
    if (l === '') return dispatch();
    if (l[0] === ':') return handlers.onComment?.(l.slice(1).replace(/^ /, ''));
    const i = l.indexOf(':');
    const field = i < 0 ? l : l.slice(0, i);
    let value = i < 0 ? '' : l.slice(i + 1);
    if (value[0] === ' ') value = value.slice(1);
    switch (field) {
      case 'event':
        event = value;
        break;
      case 'data':
        data.push(value);
        hasData = true;
        break;
      case 'id':
        if (!value.includes('\0')) lastId = value;
        break;
      case 'retry':
        if (/^\d{1,9}$/.test(value)) handlers.onRetry?.(Number(value));
        break;
      default:
        break; // unknown fields are ignored
    }
  };

  return {
    /** @param {string} chunk */
    feed(chunk) {
      let text = String(chunk);
      if (first && text.length > 0) {
        first = false;
        if (text.charCodeAt(0) === 0xfeff) text = text.slice(1);
      }
      if (sawCR && text.length > 0) {
        if (text[0] === '\n') text = text.slice(1);
        sawCR = false;
      }
      text = buffer + text;
      buffer = '';
      let start = 0;
      for (let i = 0; i < text.length; i++) {
        const c = text[i];
        if (c !== '\n' && c !== '\r') continue;
        if (dropping) dropping = false;
        else line(text.slice(start, i));
        if (c === '\r') {
          if (i + 1 < text.length) {
            if (text[i + 1] === '\n') i++;
          } else sawCR = true;
        }
        start = i + 1;
      }
      const tail = text.slice(start);
      if (dropping || tail.length > maxLine) {
        dropping = true;
        buffer = '';
      } else buffer = tail;
    },
    /** The id of the last event seen (also from events that carried no data). */
    get lastEventId() {
      return lastId;
    },
    /** Forget a half-received event (a reconnect starts clean); the last id is kept. */
    reset() {
      buffer = '';
      sawCR = false;
      dropping = false;
      event = '';
      data = [];
      hasData = false;
    },
  };
}
