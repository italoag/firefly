import { config, init as initConfig, shutDown as shutDownConfig} from './lib/config';
import express from 'express';
import bodyParser from 'body-parser';
import membersRouter from './routers/members';
import assetDefinitionsRouter from './routers/asset-definitions';
import assetInstancesRouter from './routers/asset-instances';
import paymentDefinitionsRouter from './routers/payment-definitions';
import paymentInstancesRouter from './routers/payment-instances';
import { errorHandler } from './lib/request-error';
import * as utils from './lib/utils';
import * as ipfs from './clients/ipfs';
import * as docExchange from './clients/doc-exchange';
import * as eventStreams from './clients/event-streams';
import { createLogger, LogLevelString } from 'bunyan';

const log = createLogger({ name: 'index.ts', level: utils.constants.LOG_LEVEL as LogLevelString });

export const promise = initConfig()
  .then(() => ipfs.init())
  .then(() => docExchange.init())
  .then(() => {
    eventStreams.init();
    const app = express();

    app.use(bodyParser.urlencoded({ extended: true }));
    app.use(bodyParser.json());

    app.use('/api/v1/members', membersRouter);
    app.use('/api/v1/assets/definitions', assetDefinitionsRouter);
    app.use('/api/v1/assets/instances', assetInstancesRouter);
    app.use('/api/v1/payments/definitions', paymentDefinitionsRouter);
    app.use('/api/v1/payments/instances', paymentInstancesRouter);

    app.use(errorHandler);

    const server = app.listen(config.port, () => {
      log.info(`Asset trail listening on port ${config.port} - log level "${utils.constants.LOG_LEVEL}"`);
    });

    const shutDown = () => {
      server.close();
      eventStreams.shutDown();
      shutDownConfig();
    };

    return { app, shutDown };

  }).catch(err => {
    log.error(`Failed to start asset trail. ${err}`);
  });