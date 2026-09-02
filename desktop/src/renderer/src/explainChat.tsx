/**
 * The renderer's explain-in-chat context. The route requester lives at the
 * app root — App owns the routed-request state every panel consumes — and
 * roughly twenty ErrorSurface call sites need it to route an
 * "Explain in chat" question into the AMA panel. Prop drilling the requester
 * through all of them would thread an app-root concern through every
 * intermediate surface, so the provider mounts once in App and any
 * ErrorSurface asks for it here. With no provider mounted the hook returns
 * null and the chat affordance is omitted, keeping isolated component
 * renders (and their tests) chat-free.
 */
import { createContext, useContext, type ReactNode } from 'react';
import type { AppRouteEvent } from '../../shared/ipc';

/** Issues a route request exactly as App's root route requester does. */
export type ExplainChatRequester = (event: AppRouteEvent) => void;

const ExplainChatContext = createContext<ExplainChatRequester | null>(null);

export function ExplainChatProvider({
  requestRoute,
  children,
}: {
  requestRoute: ExplainChatRequester;
  children: ReactNode;
}): React.ReactElement {
  return <ExplainChatContext.Provider value={requestRoute}>{children}</ExplainChatContext.Provider>;
}

/** The app-root route requester, or null when no provider is mounted. */
export function useExplainChat(): ExplainChatRequester | null {
  return useContext(ExplainChatContext);
}
