/*
Copyright 2026 DoorDash, Inc.

Licensed under the Apache License, Version 2.0 (the "License");
you may not use this file except in compliance with the License.
You may obtain a copy of the License at

    http://www.apache.org/licenses/LICENSE-2.0

Unless required by applicable law or agreed to in writing, software
distributed under the License is distributed on an "AS IS" BASIS,
WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
See the License for the specific language governing permissions and
limitations under the License.
*/

import { useState, type MouseEvent, type ReactNode } from 'react';
import ReactMarkdown from 'react-markdown';
import remarkGfm from 'remark-gfm';

function ConversationLink({ href, children }: { href?: string; children?: ReactNode }) {
  const [failed, setFailed] = useState(false);
  // Match the main-process external-browser policy. It validates again at
  // the IPC boundary; other schemes and credential-bearing URLs stay inert.
  let safe = false;
  try {
    const url = new URL(href ?? '');
    safe =
      url.protocol === 'https:' &&
      url.hostname !== '' &&
      url.username === '' &&
      url.password === '';
  } catch {
    // Relative paths and malformed URLs are not external browser links.
  }
  if (!safe || href === undefined) {
    return (
      <span>
        {children}
        {href ? ` (${href})` : ''}
      </span>
    );
  }

  const open = (event: MouseEvent<HTMLAnchorElement>) => {
    // Including Cmd/Ctrl-click: Chromium must never navigate this window or
    // create a new renderer window. Keyboard activation also arrives here.
    event.preventDefault();
    event.stopPropagation();
    if (event.button !== 0 && event.button !== 1) return;
    setFailed(false);
    void window.agentico
      .openExternal({ url: href })
      .then((result) => setFailed(!result.ok))
      .catch(() => setFailed(true));
  };

  return (
    <>
      <a className="md-link" href={href} title={href} onClick={open} onAuxClick={open}>
        {children}
      </a>
      {failed ? <span role="status"> Could not open link.</span> : null}
    </>
  );
}

/** Parse links structurally so URLs in code stay literal and raw HTML stays text. */
export function ConversationMarkdown({ text }: { text: string }) {
  return (
    <ReactMarkdown
      remarkPlugins={[remarkGfm]}
      components={{
        a: ConversationLink,
        // Transcript media must never fetch remote resources on render.
        img: ({ alt }) => <span>{alt}</span>,
      }}
    >
      {text}
    </ReactMarkdown>
  );
}
