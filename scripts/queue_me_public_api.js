'use strict';

const SONAR_ORIGIN = 'https://sonarcloud.io';
const GITHUB_ORIGIN = 'https://api.github.com';
const REPOSITORY = 'ben-ranford/lopper';
const MAX_RESPONSE_BYTES = 2 * 1024 * 1024;
const REQUEST_TIMEOUT_MS = 10000;
const AUDIT_TIMEOUT_MS = 60000;

async function responseJSON(response, requireEvidence) {
  requireEvidence(response?.status !== 403 && response?.status !== 429,
    'the public API refused access or reached its rate limit; retry when public access is available.');
  requireEvidence(response?.ok === true && response.redirected === false,
    'the public API returned an error or redirect.');
  requireEvidence(/^application\/json(?:;|$)/i.test(response.headers?.get('content-type') || ''),
    'the public API returned a non-JSON response.');
  const length = response.headers.get('content-length');
  requireEvidence(length === null || (/^\d+$/.test(length) && Number(length) <= MAX_RESPONSE_BYTES),
    'the public API response exceeds its size limit.');
  requireEvidence(typeof response.body?.[Symbol.asyncIterator] === 'function',
    'the public API returned an unreadable response.');
  const chunks = [];
  let size = 0;
  for await (const chunk of response.body) {
    requireEvidence(chunk instanceof Uint8Array, 'the public API returned invalid response data.');
    size += chunk.byteLength;
    requireEvidence(size <= MAX_RESPONSE_BYTES, 'the public API response exceeds its size limit.');
    chunks.push(chunk);
  }
  const buffer = Buffer.concat(chunks, size);
  return JSON.parse(new TextDecoder('utf-8', { fatal: true }).decode(buffer));
}

function endpointURL(endpoint, parameters, github, requireEvidence) {
  requireEvidence(typeof github === 'boolean' && typeof endpoint === 'string' &&
    endpoint.length > 0 && endpoint.length <= 1024,
  'the public API endpoint is outside the configured scope.');
  const parts = endpoint.split('/');
  requireEvidence(parts.every((part) => /^[\w.-]+$/.test(part) && part !== '.' && part !== '..'),
    'the public API endpoint is outside the configured scope.');
  const url = github
    ? new URL(`/repos/${REPOSITORY}/${endpoint}`, GITHUB_ORIGIN)
    : new URL(`/api/${endpoint}`, SONAR_ORIGIN);
  url.search = new URLSearchParams(parameters).toString();
  return url;
}

function createPublicAPI({ pause, fetchImpl = fetch }) {
  const requireEvidence = (condition, message) => {
    if (!condition) {
      throw pause(message);
    }
  };
  const deadline = Date.now() + AUDIT_TIMEOUT_MS;
  return async (endpoint, parameters, github = false) => {
    const remaining = deadline - Date.now();
    requireEvidence(remaining > 0, 'the audit exceeded its time limit; retry after analysis completes.');
    const url = endpointURL(endpoint, parameters, github, requireEvidence);
    const controller = new AbortController();
    let timer;
    try {
      const timeout = new Promise((_, reject) => {
        timer = setTimeout(() => {
          controller.abort();
          reject(pause('the public API timed out; retry after analysis completes.'));
        }, Math.min(REQUEST_TIMEOUT_MS, remaining));
      });
      const request = Promise.resolve(fetchImpl(url.toString(), {
        signal: controller.signal,
        redirect: 'error',
        credentials: 'omit',
        cache: 'no-store',
        headers: github
          ? { Accept: 'application/vnd.github+json', 'X-GitHub-Api-Version': '2022-11-28' }
          : { Accept: 'application/json' },
      })).then((response) => responseJSON(response, requireEvidence));
      return await Promise.race([request, timeout]);
    } catch (error) {
      if (error?.queuePauseMessage) {
        throw error;
      }
      throw pause('the public API request failed or returned malformed data.');
    } finally {
      clearTimeout(timer);
      controller.abort();
    }
  };
}

module.exports = { createPublicAPI };
