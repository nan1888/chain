const Connection = require('./connection')
const accessTokensAPI = require('./api/accessTokens')
const accountsAPI = require('./api/accounts')
const assetsAPI = require('./api/assets')
const balancesAPI = require('./api/balances')
const configAPI = require('./api/config')
const mockHsmKeysAPI = require('./api/mockHsmKeys')
const transactionsAPI = require('./api/transactions')
const transactionFeedsAPI = require('./api/transactionFeeds')
const unspentOutputsAPI = require('./api/unspentOutputs')

/**
 * The Chain API Client object is the root object for all API interactions.
 * To interact with Chain Core, a Client object must always be instantiated
 * first.
 * @class
 */
class Client {
  /**
   * constructor - create a new Chain client object capable of interacting with
   * the specified Chain Core.
   *
   * @param {String} baseUrl - Chain Core URL.
   * @param {String} token - Chain Core client token for API access.
   * @returns {Client}
   */
  constructor(baseUrl, token = '') {
    baseUrl = baseUrl || 'http://localhost:1999'
    this.connection = new Connection(baseUrl, token)

    /**
     * API actions for access tokens
     * @type {module:AccessTokensApi}
     */
    this.accessTokens = accessTokensAPI(this)

    /**
     * API actions for accounts
     * @type {module:AccountsApi}
     */
    this.accounts = accountsAPI(this)

    /**
     * API actions for assets.
     * @type {module:AssetsApi}
     */
    this.assets = assetsAPI(this)

    /**
     * API actions for balances.
     * @type {module:BalancesApi}
     */
    this.balances = balancesAPI(this)

    /**
     * API actions for config.
     * @type {module:ConfigApi}
     */
    this.config = configAPI(this)

    /**
     * @property {module:MockHsmKeysApi} keys API actions for Mock HSM keys.
     * @property {Connection} signerConnection Mock HSM signer connection.
     */
    this.mockHsm = {
      keys: mockHsmKeysAPI(this),
      signerConnection: new Connection(`${baseUrl}/mockhsm`, token)
    }

    /**
     * API actions for transactions.
     * @type {module:TransactionsApi}
     */
    this.transactions = transactionsAPI(this)

    /**
     * API actions for transaction feeds.
     * @type {module:TransactionFeedsApi}
     */
    this.transactionFeeds = transactionFeedsAPI(this)

    /**
     * API actions for unspent outputs.
     * @type {module:UnspentOutputsApi}
     */
    this.unspentOutputs = unspentOutputsAPI(this)
  }


  /**
   * Submit a request to the stored Chain Core connection.
   *
   * @param {String} path
   * @param {object} [body={}]
   * @returns {Promise}
   */
  request(path, body = {}) {
    return this.connection.request(path, body)
  }
}

module.exports = Client
