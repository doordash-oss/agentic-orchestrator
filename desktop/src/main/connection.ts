/**
 * Connection state source for the connection shell.
 *
 * // STUB(Phase 1 Task 3): real runtime gateway will drive this state.
 * Until the runtime gateway lands, this source only moves from `idle` to
 * `awaiting-gateway`; the shell renders that as the resolve-runtime stage.
 */
import type { ConnectionState } from '../shared/ipc';

export type ConnectionListener = (state: ConnectionState) => void;

export class StubConnectionSource {
  private state: ConnectionState = {
    status: 'idle',
    stage: 'resolve-runtime',
    detail: 'Preparing to locate an Agentico runtime.',
  };

  private readonly listeners = new Set<ConnectionListener>();

  getState(): ConnectionState {
    return this.state;
  }

  subscribe(listener: ConnectionListener): () => void {
    this.listeners.add(listener);
    return () => {
      this.listeners.delete(listener);
    };
  }

  /** Begin the (stubbed) connection lifecycle. */
  start(): void {
    // STUB(Phase 1 Task 3): real runtime gateway will drive this state.
    this.setState({
      status: 'awaiting-gateway',
      stage: 'resolve-runtime',
      detail: 'Runtime gateway not yet available in this build.',
    });
  }

  private setState(next: ConnectionState): void {
    this.state = next;
    for (const listener of this.listeners) {
      listener(next);
    }
  }
}
