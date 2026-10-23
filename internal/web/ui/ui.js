import { h, render, Fragment, Component, createContext } from './vendor/preact.js';
import htm from './vendor/htm.js';

export { h, render, Fragment, Component, createContext };
export {
  useState, useEffect, useLayoutEffect, useMemo, useCallback, useRef, useReducer, useContext,
} from './vendor/hooks.js';

export const html = htm.bind(h);
