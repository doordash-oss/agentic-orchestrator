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

/**
 * Copy for the Ask Agentico welcome screen: what the panel shows before the
 * first message of a conversation. Both helpers are pure functions of a Date
 * so the screen is stable within a session and deterministic under test.
 */

/** Short lines about building with agents. One is shown per day. */
export const AMA_INSPIRATIONS: readonly string[] = [
  'Describe the outcome. Let the agents work out the path.',
  'Every great feature starts as a clear sentence.',
  'Ask the question you would ask a colleague who has read everything.',
  'The best plan is the one you can explain in a paragraph.',
  'Small, well-named phases ship faster than one heroic push.',
  'Curiosity is the only prerequisite. Everything else is in the repo.',
  'Say what done looks like, and the rest becomes a pipeline.',
  'A good review is a conversation, not a verdict.',
  'Momentum comes from the next small step, taken now.',
  'You bring the judgment. Agentico brings the hands.',
];

/** Time-of-day greeting, matching the local clock of the given moment. */
export function greetingFor(now: Date): string {
  const hour = now.getHours();
  if (hour < 5) return 'Good evening.';
  if (hour < 12) return 'Good morning.';
  if (hour < 18) return 'Good afternoon.';
  return 'Good evening.';
}

/** Day-of-year index so the line rotates once a day, not once a render. */
export function inspirationFor(now: Date): string {
  const startOfYear = new Date(now.getFullYear(), 0, 1);
  const dayOfYear = Math.floor((now.getTime() - startOfYear.getTime()) / 86_400_000);
  const index =
    ((dayOfYear % AMA_INSPIRATIONS.length) + AMA_INSPIRATIONS.length) % AMA_INSPIRATIONS.length;
  return AMA_INSPIRATIONS[index] ?? AMA_INSPIRATIONS[0] ?? '';
}
