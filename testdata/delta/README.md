# Data for testing librsync-go deltas

Reference delta files (`*.delta`) were created using the original (C version)
`rdiff`.

* `000.old`/`000.new`: Both files are equal.
* `001.old`/`001.new`: The new file was created by appending some data to the
  old file.
* `002.old`/`002.new`: The new file was created by prepending some data to the
  old file.
* `003.old`/`003.new`: The new file was created by inserting some data in the
  middle of the old file.
* `004.old`/`004.new`: Files of same size, with some smallish sequences of bytes
  arbitrarily changed on the new one.
* `005.old`/`005.new`: New file was created by removing some data from the
  beginning, middle and end of the old file.
* `006.old`/`006.new`: Small files crafted to exercise the case in which there
  is a match of the final block (with length less than the block length). This
  happens when using a block length of 2.