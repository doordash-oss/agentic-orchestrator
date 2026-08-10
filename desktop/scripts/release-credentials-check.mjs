// Backward-compatible release credential entry point for local operators.
import { runReleasePreflight } from './release-preflight.mjs';

try {
  const evidence = runReleasePreflight();
  console.log(`release credentials and local prerequisites verified for ${evidence.tag}`);
} catch (error) {
  console.error(error instanceof Error ? error.message : String(error));
  process.exitCode = 1;
}
