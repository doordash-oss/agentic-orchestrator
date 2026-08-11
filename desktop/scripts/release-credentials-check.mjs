// Backward-compatible release credential entry point for local operators.
import { cleanupReleaseWorkspace, runReleasePreflight } from './release-preflight.mjs';

let evidence;
try {
  evidence = runReleasePreflight();
  console.log(`release credentials and local prerequisites verified for ${evidence.tag}`);
} catch (error) {
  console.error(error instanceof Error ? error.message : String(error));
  process.exitCode = 1;
} finally {
  if (evidence !== undefined) cleanupReleaseWorkspace({ evidence });
}
