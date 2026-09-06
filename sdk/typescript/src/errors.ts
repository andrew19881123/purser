/**
 * Error classes for the Purser TypeScript SDK.
 */

/** Base error for all Purser API errors. */
export class PurserError extends Error {
  constructor(
    public readonly statusCode: number,
    message: string,
    public readonly errorType?: string,
  ) {
    super(message);
    this.name = 'PurserError';
    // Maintain proper prototype chain in transpiled ES5 and older targets.
    Object.setPrototypeOf(this, new.target.prototype);
  }
}

/** Thrown when the server returns 404 Not Found. */
export class NotFoundError extends PurserError {
  constructor(message: string) {
    super(404, message, 'not_found');
    this.name = 'NotFoundError';
    Object.setPrototypeOf(this, new.target.prototype);
  }
}

/** Thrown when the server returns 409 Conflict. */
export class ConflictError extends PurserError {
  constructor(message: string) {
    super(409, message, 'conflict');
    this.name = 'ConflictError';
    Object.setPrototypeOf(this, new.target.prototype);
  }
}

/**
 * Thrown when the server returns 402 Payment Required.
 * The `feature` property names the enterprise entitlement that was missing.
 */
export class LicenseRequiredError extends PurserError {
  constructor(
    message: string,
    public readonly feature?: string,
  ) {
    super(402, message, 'license_required');
    this.name = 'LicenseRequiredError';
    Object.setPrototypeOf(this, new.target.prototype);
  }
}
