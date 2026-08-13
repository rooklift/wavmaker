package wavmaker

import (
	"encoding/binary"
	"fmt"
	"io"
	"math"
	"os"
	"strings"
)

const PREFERRED_FREQ = 44100

type WAV struct {
	FmtChunk FmtChunk_Struct
	DataChunk DataChunk_Struct
}

type FmtChunk_Struct struct {
	Size uint32
	AudioFormat uint16
	NumChannels uint16
	SampleRate uint32
	ByteRate uint32
	BlockAlign uint16
	BitsPerSample uint16
}

type DataChunk_Struct struct {
	Size uint32
	Data []byte
}

var have_warned_get_out_of_bounds bool = false
var have_warned_set_out_of_bounds bool = false
var have_warned_clipping bool = false


// ------------------------------------- EXPOSED METHODS


func (wav *WAV) String() string {
	length := float64(wav.FrameCount()) / float64(wav.FmtChunk.SampleRate)
	return fmt.Sprintf("<WAV: %d channel, %d bit, %d Hz, %d frames, %.2f seconds>",
		wav.FmtChunk.NumChannels,
		wav.FmtChunk.BitsPerSample,
		wav.FmtChunk.SampleRate,
		wav.FrameCount(),
		length,
	)
}


func (wav *WAV) FrameCount() uint32 {
	return wav.DataChunk.Size / uint32(wav.FmtChunk.BlockAlign)
}


func (wav *WAV) Copy() *WAV {

	var new_wav WAV

	new_wav.FmtChunk = wav.FmtChunk
	new_wav.DataChunk.Size = wav.DataChunk.Size
	new_wav.DataChunk.Data = make([]byte, len(wav.DataChunk.Data))
	copy(new_wav.DataChunk.Data, wav.DataChunk.Data)

	if wav.sanitycheck() != nil {
		panic("newly copied WAV was not valid")
	}

	return &new_wav
}


func (original *WAV) Stretched(new_frame_count uint32) *WAV {

	if new_frame_count == original.FrameCount() {
		return original.Copy()
	}

	new_wav := New(new_frame_count)
	original_frame_count := original.FrameCount()

	if new_frame_count == 0 || original_frame_count == 0 {
		return new_wav
	}

	// With only one frame at either end, there is no interval to interpolate. Preserve the first sample.
	if new_frame_count == 1 {
		left, right := original.Get(0)
		new_wav.Set(0, left, right)
		return new_wav
	}
	if original_frame_count == 1 {
		left, right := original.Get(0)
		for n := uint32(0); n < new_frame_count; n++ {
			new_wav.Set(n, left, right)
		}
		return new_wav
	}

	// Treat the operation as resampling rather than time stretching. Keeping
	// the sample rate unchanged deliberately changes both duration and pitch.
	// A Lanczos-windowed sinc retains considerably more high-frequency detail
	// than linear interpolation. When reducing the frame count, cutoff also
	// acts as an anti-aliasing low-pass filter.
	step := float64(original_frame_count - 1) / float64(new_frame_count - 1)
	cutoff := math.Min(1, 1 / step)
	const lobes = 16.0
	support := lobes / cutoff

	for n := uint32(0); n < new_frame_count; n++ {
		position := float64(n) * step
		first := int64(math.Ceil(position - support))
		last := int64(math.Floor(position + support))
		if first < 0 {
			first = 0
		}
		if last >= int64(original_frame_count) {
			last = int64(original_frame_count) - 1
		}

		var left_sum, right_sum, weight_sum float64
		for source_frame := first; source_frame <= last; source_frame++ {
			distance := position - float64(source_frame)
			weight := cutoff * sinc(cutoff * distance) * sinc(distance / support)
			left, right := original.Get(uint32(source_frame))
			left_sum += float64(left) * weight
			right_sum += float64(right) * weight
			weight_sum += weight
		}

		// Normalising the truncated kernel avoids a volume dip at the ends.
		if math.Abs(weight_sum) > 1e-12 {
			left_sum /= weight_sum
			right_sum /= weight_sum
		}
		new_wav.Set(n, sample_from_float(left_sum), sample_from_float(right_sum))
	}

	return new_wav
}


func (wav *WAV) StretchedRelative(multiplier float64) *WAV {

	old_framecount_f := float64(wav.FrameCount())
	new_framecount_f := old_framecount_f * multiplier

	new_framecount := uint32(new_framecount_f)

	return wav.Stretched(new_framecount)
}


func (wav *WAV) Save(filename string) error {

	outfile, err := os.Create(filename)
	if outfile != nil {
		defer outfile.Close()
	}
	if err != nil {
		return fmt.Errorf("Couldn't create output file '%s': %v", filename, err)
	}

	filesize := 36 + wav.DataChunk.Size

	// Conceptually one might think of strings as being big endian, but because
	// they are comprised of byte-sized units, they have no endianness at all.

	bo := binary.LittleEndian

	var write_err error

	write := func(data interface{}) {
		if write_err == nil {
			write_err = binary.Write(outfile, bo, data)
		}
	}

	write([]byte("RIFF"))
	write(&filesize)
	write([]byte("WAVE"))
	write([]byte("fmt "))
	write(&wav.FmtChunk.Size)
	write(&wav.FmtChunk.AudioFormat)
	write(&wav.FmtChunk.NumChannels)
	write(&wav.FmtChunk.SampleRate)
	write(&wav.FmtChunk.ByteRate)
	write(&wav.FmtChunk.BlockAlign)
	write(&wav.FmtChunk.BitsPerSample)
	write([]byte("data"))
	write(&wav.DataChunk.Size)
	write(wav.DataChunk.Data)

	if write_err != nil {
		return fmt.Errorf("Couldn't write to output file '%s': %v", filename, write_err)
	}

	err = wav.sanitycheck()
	if err != nil {
		fmt.Fprintf(os.Stderr, "Warning: while saving '%s', %v\n", filename, err)
	}

	return nil
}


func (wav *WAV) Set(frame uint32, left, right int16) {

	// Assumes the wav is 16-bit stereo

	if frame >= wav.DataChunk.Size / 4 {
		if have_warned_set_out_of_bounds == false {
			have_warned_set_out_of_bounds = true
			fmt.Fprintf(os.Stderr, "Warning: out of bounds Set(). No further such warnings shall be given.\n")
		}
		return
	}

	n := frame * 4

	// Reminder to self, humans and compilers think in big-endian but the storage is little-endian...

	wav.DataChunk.Data[n + 0] = byte(left & 0xff)		// The less-significant byte
	wav.DataChunk.Data[n + 1] = byte(left >> 8)			// The more-significant byte

	wav.DataChunk.Data[n + 2] = byte(right & 0xff)		// The less-significant byte
	wav.DataChunk.Data[n + 3] = byte(right >> 8)		// The more-significant byte
}


func (wav *WAV) Get(frame uint32) (int16, int16) {

	// Assumes the wav is 16-bit stereo

	if frame >= wav.DataChunk.Size / 4 {
		if have_warned_get_out_of_bounds == false {
			have_warned_get_out_of_bounds = true
			fmt.Fprintf(os.Stderr, "Warning: out of bounds Get(). No further such warnings shall be given.\n")
		}
		return 0, 0
	}

	n := frame * 4

	left  := int16(wav.DataChunk.Data[n + 0]) | (int16(wav.DataChunk.Data[n + 1]) << 8)
	right := int16(wav.DataChunk.Data[n + 2]) | (int16(wav.DataChunk.Data[n + 3]) << 8)

	return left, right
}


func (target *WAV) Add(t_loc uint32, source *WAV, s_loc uint32, frames uint32, volume float64, fadeout uint32) {
	target.Insert(t_loc, source, s_loc, frames, volume, fadeout, true)
}


func (target *WAV) Replace(t_loc uint32, source *WAV, s_loc uint32, frames uint32, volume float64, fadeout uint32) {
	target.Insert(t_loc, source, s_loc, frames, volume, fadeout, false)
}


func (target *WAV) Insert(t_loc uint32, source *WAV, s_loc uint32, frames uint32, volume float64, fadeout uint32, additive bool) {

	// This function adds the source wav to the target, with various options. It is highly relevant to my related
	// Trackmaker project, and indeed perhaps includes too much logic specific to that. If things get out of hand,
	// it should just be moved into that project, and a simplified feature-reduced version placed here.

	if frames == 0 {
		return
	}

	t := t_loc
	s := s_loc
	frames_added := uint32(0)

	var clipped bool = false

	for {
		if t >= target.FrameCount() {
			break
		}
		if s >= source.FrameCount() {
			break
		}

		target_left, target_right := int16(0), int16(0)
		if additive {
			target_left, target_right = target.Get(t)
		}

		source_left, source_right := source.Get(s)

		frames_to_go := frames - frames_added
		if frames_to_go < fadeout {
			fade_multiplier := float64(frames_to_go) / float64(fadeout)

			source_left  = int16(fade_multiplier * float64(source_left))
			source_right = int16(fade_multiplier * float64(source_right))
		}

		var new_left_32, new_right_32 int32

		if volume == 1.0 {
			new_left_32  = int32(target_left)  + int32(source_left)
			new_right_32 = int32(target_right) + int32(source_right)
		} else {
			new_left_32  = int32(target_left)  + int32(float64(source_left) * volume)
			new_right_32 = int32(target_right) + int32(float64(source_right) * volume)
		}

		if new_left_32  < -32768 { new_left_32  = -32768 ; clipped = true }
		if new_left_32  >  32767 { new_left_32  =  32767 ; clipped = true }
		if new_right_32 < -32768 { new_right_32 = -32768 ; clipped = true }
		if new_right_32 >  32767 { new_right_32 =  32767 ; clipped = true }

		new_left  := int16(new_left_32)
		new_right := int16(new_right_32)

		target.Set(t, new_left, new_right)

		t++
		s++

		frames_added++
		if frames_added >= frames {
			break
		}
	}

	if clipped == true && have_warned_clipping == false {
		have_warned_clipping = true
		fmt.Fprintf(os.Stderr, "Warning: clipping occurred in Add(). No further such warnings shall be given.\n")
	}
}


func (wav *WAV) FadeSamples(frames_to_fade uint32) {

	if frames_to_fade <= 0 {
		return
	}

	total_frames := wav.FrameCount()

	if total_frames < 2 {
		return
	}

	if frames_to_fade > total_frames {
		frames_to_fade = total_frames
	}

	for n := total_frames - 1 ; n > total_frames - frames_to_fade ; n-- {	// Use > not >= because of uint wrap-around

		multiplier := float64(total_frames - n) / float64(frames_to_fade)

		old_left, old_right := wav.Get(n)

		new_left_f  := float64(old_left)  * multiplier
		new_right_f := float64(old_right) * multiplier

		new_left  := int16(new_left_f)
		new_right := int16(new_right_f)

		wav.Set(n, new_left, new_right)
	}
}


func (wav *WAV) FadeFraction(fraction float64) {		// e.g. an argument of 0.25 fades out the final 25%

	if fraction <= 0 {
		return
	}
	if fraction > 1 {
		fraction = 1
	}

	total_frames := wav.FrameCount()
	frames_to_fade := uint32(float64(total_frames) * fraction)

	wav.FadeSamples(frames_to_fade)
}


// ------------------------------------- EXPOSED FUNCTIONS


func Load(filename string) (*WAV, error) {

	var infile *os.File
	var err error
	var buf [4]byte
	var wav WAV
	var got_fmt, got_data bool

	infile, err = os.Open(filename)
	if infile != nil {
		defer infile.Close()
	}
	if err != nil {
		return &wav, fmt.Errorf("load_wav() couldn't load '%s': %v", filename, err)
	}

	// --------------------

	err = binary.Read(infile, binary.LittleEndian, &buf)
	if err != nil {
		return &wav, fmt.Errorf("load_wav() couldn't read RIFF bytes: %v", err)
	}
	if buf != [4]byte{'R', 'I', 'F', 'F'} {
		return &wav, fmt.Errorf("load_wav() found bytes 0-3 != RIFF")
	}

	// --------------------

	var totalsize uint32

	err = binary.Read(infile, binary.LittleEndian, &totalsize)
	if err != nil {
		return &wav, fmt.Errorf("load_wav() couldn't read total file size: %v", err)
	}

	// --------------------

	err = binary.Read(infile, binary.LittleEndian, &buf)
	if err != nil {
		return &wav, fmt.Errorf("load_wav() couldn't read WAVE bytes: %v", err)
	}
	if buf != [4]byte{'W', 'A', 'V', 'E'} {
		return &wav, fmt.Errorf("load_wav() found bytes 8-11 != WAVE")
	}

	// --------------------

	for {

		err = binary.Read(infile, binary.LittleEndian, &buf)
		if err != nil {
			return &wav, fmt.Errorf("load_wav() couldn't read chunk's starting bytes: %v", err)
		}

		if buf == [4]byte{'f', 'm', 't', ' '} {
			wav.FmtChunk, err = load_fmt(infile)
			if err != nil {
				return &wav, err
			}
			got_fmt = true
		} else if buf == [4]byte{'d', 'a', 't', 'a'} {
			wav.DataChunk, err = load_data(infile)
			if err != nil {
				return &wav, err
			}
			got_data = true
		} else {
			err = skip_chunk(infile, buf)
			if err != nil {
				return &wav, err
			}
		}

		if got_fmt && got_data {
			break
		}
	}

	// --------------------

	err = wav.sanitycheck()
	if err != nil {
		return &wav, err
	}

	err = wav.convert(filename)
	if err != nil {
		return &wav, err
	}

	return &wav, nil
}


func New(frames uint32) *WAV {

	var wav WAV

	wav.FmtChunk.Size = 16
	wav.FmtChunk.AudioFormat = 1
	wav.FmtChunk.NumChannels = 2
	wav.FmtChunk.SampleRate = PREFERRED_FREQ
	wav.FmtChunk.ByteRate = PREFERRED_FREQ * 4		// Bytes per second; we are using 4 bytes per frame
	wav.FmtChunk.BlockAlign = 4
	wav.FmtChunk.BitsPerSample = 16

	wav.DataChunk.Size = uint32(wav.FmtChunk.BitsPerSample / 8) * frames * uint32(wav.FmtChunk.NumChannels)
	wav.DataChunk.Data = make([]byte, wav.DataChunk.Size)

	if wav.sanitycheck() != nil {
		panic("failed to create a valid WAV")
	}

	return &wav
}


// ------------------------------------- NON-EXPOSED FUNCTIONS


func skip_chunk(infile *os.File, chunk_name [4]byte) error {

	var chunk_size uint32
	var err error
	var buf byte

	err = binary.Read(infile, binary.LittleEndian, &chunk_size)
	if err != nil {
		return fmt.Errorf("skip_chunk() couldn't read '%s' chunk size: %v", chunk_name, err)
	}

	for n := uint32(0) ; n < chunk_size ; n++ {
		err = binary.Read(infile, binary.LittleEndian, &buf)
		if err != nil {
			return fmt.Errorf("skip_chunk() couldn't read '%s' chunk contents: %v", chunk_name, err)
		}
	}

	return nil
}


func load_fmt(infile *os.File) (FmtChunk_Struct, error) {

	var chunk FmtChunk_Struct
	var err error

	err = binary.Read(infile, binary.LittleEndian, &chunk)
	if err != nil {
		return chunk, fmt.Errorf("load_fmt() couldn't read fmt chunk: %v", err)
	}

	// Some common variants (e.g. from ffmpeg or sox) have an 18 or 40 byte fmt chunk,
	// where the extra bytes are an extension we don't care about. Skip past them, and
	// normalise the recorded size to 16 so the rest of the code is happy.

	if chunk.Size > 16 {
		_, err = infile.Seek(int64(chunk.Size - 16), io.SeekCurrent)
		if err != nil {
			return chunk, fmt.Errorf("load_fmt() couldn't skip fmt chunk extension: %v", err)
		}
		chunk.Size = 16
	} else if chunk.Size < 16 {
		return chunk, fmt.Errorf("load_fmt() fmt chunk size was %d (expected 16 or more)", chunk.Size)
	}

	return chunk, nil
}


func load_data(infile *os.File) (DataChunk_Struct, error) {

	var chunk DataChunk_Struct
	var err error

	err = binary.Read(infile, binary.LittleEndian, &chunk.Size)
	if err != nil {
		return chunk, fmt.Errorf("load_data() couldn't read chunk size: %v", err)
	}

	chunk.Data = make([]byte, chunk.Size)

	err = binary.Read(infile, binary.LittleEndian, chunk.Data)
	if err != nil {
		return chunk, fmt.Errorf("load_data() couldn't read data: %v", err)
	}

	return chunk, nil
}


func sinc(x float64) float64 {
	if math.Abs(x) < 1e-12 {
		return 1
	}
	x *= math.Pi
	return math.Sin(x) / x
}


func sample_from_float(sample float64) int16 {
	sample = math.Round(sample)
	if sample > math.MaxInt16 {
		return math.MaxInt16
	}
	if sample < math.MinInt16 {
		return math.MinInt16
	}
	return int16(sample)
}


// ------------------------------------- NON-EXPOSED METHODS


func (wav *WAV) convert(filename string) error {		// Filename given just for printing useful info

	// Remember, this is an in-place conversion, we can't just set *wav ptr to be something else.
	// Rather, the struct that *wav points to itself needs to be modified.

	// We want 16-bit audio:

	if wav.FmtChunk.BitsPerSample != 16 {

		if wav.FmtChunk.BitsPerSample != 8 {
			return fmt.Errorf("convert_wav(): bits per sample in '%s' was not 8 or 16", filename)
		}

		fmt.Fprintf(os.Stderr, "Converting '%s' to 16 bit...\n", filename)

		new_data := make([]byte, wav.DataChunk.Size * 2)

		for n := uint32(0) ; n < wav.DataChunk.Size ; n++ {

			old_val := int32(wav.DataChunk.Data[n])

			new_val := ((old_val - 128) * 256) + old_val

			// Reminder to self, humans and compilers think in big-endian but the storage is little-endian...

			new_data[n * 2] = byte(new_val & 0xff)			// The less-significant bytes
			new_data[n * 2 + 1] = byte(new_val >> 8)		// The more-significant bytes
		}

		wav.FmtChunk.BitsPerSample = 16

		wav.FmtChunk.ByteRate *= 2
		wav.FmtChunk.BlockAlign *= 2

		wav.DataChunk.Data = new_data
		wav.DataChunk.Size *= 2
	}

	// We want stereo:

	if wav.FmtChunk.NumChannels == 1 {

		fmt.Fprintf(os.Stderr, "Converting '%s' to stereo...\n", filename)

		new_data := make([]byte, wav.DataChunk.Size * 2)

		for n := uint32(0) ; n < wav.DataChunk.Size ; n += 2 {

			// Things are guaranteed 16-bit at this point, so the following is right...

			new_data[n * 2] = wav.DataChunk.Data[n]
			new_data[n * 2 + 2] = wav.DataChunk.Data[n]

			new_data[n * 2 + 1] = wav.DataChunk.Data[n + 1]
			new_data[n * 2 + 3] = wav.DataChunk.Data[n + 1]
		}

		wav.FmtChunk.NumChannels = 2

		wav.FmtChunk.ByteRate *= 2
		wav.FmtChunk.BlockAlign *= 2

		wav.DataChunk.Data = new_data
		wav.DataChunk.Size *= 2
	}

	// We want 44100 Hz:

	if wav.FmtChunk.SampleRate != PREFERRED_FREQ {

		new_frame_count := uint32(uint64(wav.FrameCount()) * PREFERRED_FREQ / uint64(wav.FmtChunk.SampleRate))
		fmt.Fprintf(os.Stderr, "Converting '%s' to %d Hz ", filename, PREFERRED_FREQ)
		fmt.Fprintf(os.Stderr, " (%d -> %d frames)...\n", wav.FrameCount(), new_frame_count)
		*wav = *wav.Stretched(new_frame_count)
	}

	// Final sanity check:

	err := wav.sanitycheck()
	if err != nil {
		return fmt.Errorf("convert_wav(): seemed to succeed, but: %v", err)
	}

	return nil
}


func (wav *WAV) sanitycheck() error {

	s := make([]string, 0)

	if wav.FmtChunk.Size != 16 {
		s = append(s, "fmt chunk size != 16")
	}

	if wav.FmtChunk.AudioFormat != 1 {
		s = append(s, "audio format != 1 (PCM)")
	}

	if wav.FmtChunk.NumChannels > 2 {
		s = append(s, "num channels > 2")
	}

	if wav.FmtChunk.ByteRate != wav.FmtChunk.SampleRate * uint32(wav.FmtChunk.NumChannels) * uint32(wav.FmtChunk.BitsPerSample) / 8 {
		s = append(s, "byte rate did not match other fmt fields")
	}

	if wav.FmtChunk.BlockAlign != wav.FmtChunk.NumChannels * wav.FmtChunk.BitsPerSample / 8 {
		s = append(s, "block align did not match other fmt fields")
	}

	if wav.DataChunk.Size != uint32(len(wav.DataChunk.Data)) {
		s = append(s, "data chunk size did not match amount of data read")
	}

	if len(s) > 0 {
		msg := "sanitycheck(): " + strings.Join(s, ", ")
		return fmt.Errorf("%v", msg)
	}

	return nil
}
