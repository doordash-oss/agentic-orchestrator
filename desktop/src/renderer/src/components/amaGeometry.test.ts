import { describe, expect, it } from 'vitest';
import { defaultAmaGeometry } from '../../../shared/ipc';
import { clampAmaGeometry, dragAmaGeometry, resizeAmaGeometry } from './amaGeometry';

const WINDOW = { width: 1440, height: 900 };

describe('clampAmaGeometry', () => {
  it('leaves an in-window placement untouched', () => {
    expect(clampAmaGeometry(defaultAmaGeometry(), WINDOW)).toEqual(defaultAmaGeometry());
  });

  it('pulls a placement written on a larger display back inside the window', () => {
    expect(
      clampAmaGeometry({ right: 3200, bottom: 2000, width: 404, height: 560 }, WINDOW),
    ).toEqual({ right: 1036, bottom: 340, width: 404, height: 560 });
  });

  it('shrinks a panel larger than the window to the window', () => {
    expect(clampAmaGeometry({ right: 20, bottom: 20, width: 900, height: 800 }, WINDOW)).toEqual({
      right: 20,
      bottom: 20,
      width: 900,
      height: 800,
    });
    expect(
      clampAmaGeometry(
        { right: 20, bottom: 20, width: 900, height: 800 },
        { width: 400, height: 480 },
      ),
    ).toEqual({ right: 0, bottom: 0, width: 400, height: 480 });
  });

  it('holds the usable minimum unless the window itself is smaller', () => {
    expect(clampAmaGeometry({ right: 0, bottom: 0, width: 10, height: 10 }, WINDOW)).toEqual({
      right: 0,
      bottom: 0,
      width: 320,
      height: 240,
    });
    expect(
      clampAmaGeometry({ right: 0, bottom: 0, width: 10, height: 10 }, { width: 200, height: 120 }),
    ).toEqual({ right: 0, bottom: 0, width: 200, height: 120 });
  });
});

describe('dragAmaGeometry', () => {
  it('keeps the panel anchored to the bottom-right corner it is dragged from', () => {
    expect(dragAmaGeometry(defaultAmaGeometry(), { x: -100, y: -50 }, WINDOW)).toMatchObject({
      right: 120,
      bottom: 70,
    });
  });

  it('stops at the window edges', () => {
    expect(dragAmaGeometry(defaultAmaGeometry(), { x: 500, y: 500 }, WINDOW)).toMatchObject({
      right: 0,
      bottom: 0,
    });
  });
});

describe('resizeAmaGeometry', () => {
  it('grows away from the anchor on leading and top edges', () => {
    expect(resizeAmaGeometry(defaultAmaGeometry(), 'nw', { x: -40, y: -30 }, WINDOW)).toEqual({
      right: 20,
      bottom: 20,
      width: 444,
      height: 590,
    });
  });

  it('moves the anchor with the pointer on trailing and bottom edges', () => {
    expect(resizeAmaGeometry(defaultAmaGeometry(), 'se', { x: 15, y: 10 }, WINDOW)).toEqual({
      right: 5,
      bottom: 10,
      width: 419,
      height: 570,
    });
  });

  it('never resizes past the minimum or the window', () => {
    expect(resizeAmaGeometry(defaultAmaGeometry(), 'w', { x: 4000, y: 0 }, WINDOW)).toMatchObject({
      width: 320,
    });
    expect(resizeAmaGeometry(defaultAmaGeometry(), 'n', { x: 0, y: -4000 }, WINDOW)).toMatchObject({
      height: 900,
    });
  });
});
